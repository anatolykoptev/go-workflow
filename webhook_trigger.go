package workflow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// WebhookAuth selects the authentication strategy for a webhook trigger.
type WebhookAuth int

const (
	// WebhookAuthNone leaves the endpoint open. Intended for development —
	// the engine logs a warning at registration when this mode is selected.
	WebhookAuthNone WebhookAuth = iota
	// WebhookAuthBearer requires "Authorization: Bearer <secret>" with a
	// constant-time comparison.
	WebhookAuthBearer
	// WebhookAuthHMAC requires SignHeader to contain the hex-encoded
	// HMAC-SHA256 of the raw request body, computed with Secret as the key.
	// Mirrors GitHub's X-Hub-Signature-256 shape.
	WebhookAuthHMAC
)

// webhookMaxBodyBytes caps a single webhook request body. Set to 10 MiB —
// matches GitHub's documented webhook payload ceiling and bounds memory.
const webhookMaxBodyBytes = 10 * 1024 * 1024

// WebhookTrigger fires a named template instantiation when an HTTP request
// hits Path. Path must be unique per registered trigger. Symmetric with
// EventTrigger (hook events) and the Scheduler (cron) — three families
// that all instantiate templates and start workflows.
type WebhookTrigger struct {
	Path       string
	Template   string
	VarMapper  func(*http.Request, []byte) (map[string]any, error)
	AuthMode   WebhookAuth
	Secret     string
	SignHeader string // for WebhookAuthHMAC, e.g. "X-Hub-Signature-256"
	Owner      string // owner string assigned to instantiated workflows
}

// RegisterWebhooks attaches every trigger to mux as a POST handler at its
// Path. Returns an error on misconfiguration (duplicate path, missing
// secret for an auth mode that needs it, etc.). Workflows start
// asynchronously — the HTTP response returns 202 with the workflow ID
// before the workflow finishes.
func (e *Engine) RegisterWebhooks(mux *http.ServeMux, runtime *TemplateRuntime, triggers []WebhookTrigger) error {
	if mux == nil {
		return errors.New("webhook: mux is nil")
	}
	if runtime == nil {
		return errors.New("webhook: template runtime is nil")
	}
	seen := make(map[string]bool)
	for i := range triggers {
		t := triggers[i]
		if t.Path == "" {
			return fmt.Errorf("webhook[%d]: empty path", i)
		}
		if seen[t.Path] {
			return fmt.Errorf("webhook: duplicate path %q", t.Path)
		}
		seen[t.Path] = true
		if err := validateWebhookAuth(t); err != nil {
			return fmt.Errorf("webhook[%s]: %w", t.Path, err)
		}
		if t.AuthMode == WebhookAuthNone {
			e.log().Warn("webhook registered without auth — dev mode only",
				"component", "workflow",
				"path", t.Path,
				"template", t.Template,
			)
		}
		mux.HandleFunc(t.Path, e.makeWebhookHandler(t, runtime))
	}
	return nil
}

func validateWebhookAuth(t WebhookTrigger) error {
	switch t.AuthMode {
	case WebhookAuthNone:
		return nil
	case WebhookAuthBearer:
		if t.Secret == "" {
			return errors.New("bearer auth requires Secret")
		}
		return nil
	case WebhookAuthHMAC:
		if t.Secret == "" {
			return errors.New("hmac auth requires Secret")
		}
		if t.SignHeader == "" {
			return errors.New("hmac auth requires SignHeader")
		}
		return nil
	default:
		return fmt.Errorf("unknown auth mode %d", t.AuthMode)
	}
}

func (e *Engine) makeWebhookHandler(t WebhookTrigger, runtime *TemplateRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m := e.getMetrics(); m != nil {
			m.WebhooksReceived.Add(1)
		}
		body, ok := readWebhookBody(e, w, r)
		if !ok {
			return
		}
		if !checkWebhookAuth(t, r, body) {
			webhookReject(e, w, http.StatusUnauthorized, "unauthorized", "auth check failed")
			return
		}
		params, ok := extractWebhookParams(e, w, t, r, body)
		if !ok {
			return
		}
		e.startWebhookWorkflow(w, t, runtime, params)
	}
}

// readWebhookBody validates method + size and returns the buffered body.
// Writes an error response and returns false on rejection.
func readWebhookBody(e *Engine, w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		webhookReject(e, w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return nil, false
	}
	// Cap body BEFORE auth so a malicious giant payload can't OOM us.
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBodyBytes+1))
	if err != nil {
		webhookReject(e, w, http.StatusBadRequest, "body_read_failed", err.Error())
		return nil, false
	}
	if len(body) > webhookMaxBodyBytes {
		webhookReject(e, w, http.StatusRequestEntityTooLarge, "body_too_large",
			fmt.Sprintf("body exceeds %d bytes", webhookMaxBodyBytes))
		return nil, false
	}
	return body, true
}

// extractWebhookParams runs the trigger's VarMapper (or default JSON mapper)
// to derive template params from the request.
func extractWebhookParams(e *Engine, w http.ResponseWriter, t WebhookTrigger, r *http.Request, body []byte) (map[string]any, bool) {
	mapper := t.VarMapper
	if mapper == nil {
		mapper = defaultVarMapper
	}
	params, err := mapper(r, body)
	if err != nil {
		webhookReject(e, w, http.StatusBadRequest, "var_mapper_failed", err.Error())
		return nil, false
	}
	return params, true
}

// startWebhookWorkflow instantiates the trigger's template, persists the new
// workflow, kicks off async execution, and writes a 202 response.
func (e *Engine) startWebhookWorkflow(w http.ResponseWriter, t WebhookTrigger, runtime *TemplateRuntime, params map[string]any) {
	owner := t.Owner
	if owner == "" {
		owner = "webhook:" + t.Path
	}
	workflowID := fmt.Sprintf("wf-webhook-%s-%d", sanitizeForID(t.Template), now())
	wf, err := runtime.Instantiate(t.Template, workflowID, owner, params)
	if err != nil {
		webhookReject(e, w, http.StatusInternalServerError, "instantiate_failed", err.Error())
		return
	}
	if err := e.store.Save(wf); err != nil {
		webhookReject(e, w, http.StatusInternalServerError, "store_save_failed", err.Error())
		return
	}
	if err := e.StartAsync(context.Background(), workflowID); err != nil {
		webhookReject(e, w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"workflow_id": workflowID,
		"template":    t.Template,
		"status":      "accepted",
	})
}

// checkWebhookAuth applies the trigger's auth mode to the request +
// already-buffered body. Returns false on any failure; never panics.
func checkWebhookAuth(t WebhookTrigger, r *http.Request, body []byte) bool {
	switch t.AuthMode {
	case WebhookAuthNone:
		return true
	case WebhookAuthBearer:
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		return subtle.ConstantTimeCompare([]byte(got), []byte(t.Secret)) == 1
	case WebhookAuthHMAC:
		raw := r.Header.Get(t.SignHeader)
		// GitHub-style signatures are prefixed with "sha256="; strip it.
		raw = strings.TrimPrefix(raw, "sha256=")
		expected, err := hex.DecodeString(raw)
		if err != nil {
			return false
		}
		mac := hmac.New(sha256.New, []byte(t.Secret))
		mac.Write(body)
		actual := mac.Sum(nil)
		return hmac.Equal(expected, actual)
	}
	return false
}

func defaultVarMapper(_ *http.Request, body []byte) (map[string]any, error) {
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	return out, nil
}

func webhookReject(e *Engine, w http.ResponseWriter, status int, code, message string) {
	if m := e.getMetrics(); m != nil {
		m.WebhooksRejected.Add(1)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}

// sanitizeForID strips characters unfit for a workflow ID slug. Conservative:
// only [a-zA-Z0-9_-] survive.
func sanitizeForID(s string) string {
	if s == "" {
		return "anon"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
