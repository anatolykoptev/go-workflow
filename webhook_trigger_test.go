package workflow

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestTemplate(t *testing.T, dir, name string) {
	t.Helper()
	tmpl := map[string]any{
		"name":        name,
		"description": "test webhook target",
		"steps": []map[string]any{
			{
				"id":     "noop",
				"kind":   "noop",
				"config": map[string]any{},
			},
		},
	}
	raw, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
}

// noopWebhookEngine builds an engine + template runtime for webhook tests.
// We use the StepNoop primitive so no executors-with-side-effects are needed.
func noopWebhookEngine(t *testing.T) (*Engine, *TemplateRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	writeTestTemplate(t, dir, "tpl_a")
	store := newTestStore(t)
	engine := NewEngine(store)
	runtime := NewTemplateRuntime(dir)
	return engine, runtime, dir
}

func TestWebhook_NoAuth_HappyPath(t *testing.T) {
	t.Parallel()

	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/a", Template: "tpl_a", AuthMode: WebhookAuthNone},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	body := bytes.NewBufferString(`{"hello":"world"}`)
	req := httptest.NewRequest(http.MethodPost, "/trigger/a", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["template"] != "tpl_a" || resp["status"] != "accepted" {
		t.Errorf("resp = %+v", resp)
	}
	if !strings.HasPrefix(resp["workflow_id"], "wf-webhook-tpl_a-") {
		t.Errorf("workflow_id = %q", resp["workflow_id"])
	}
}

func TestWebhook_Bearer_Success(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/b", Template: "tpl_a", AuthMode: WebhookAuthBearer, Secret: "topsecret"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/trigger/b", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer topsecret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
}

func TestWebhook_Bearer_WrongSecret(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/c", Template: "tpl_a", AuthMode: WebhookAuthBearer, Secret: "topsecret"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/trigger/c", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestWebhook_HMAC_Success(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/d", Template: "tpl_a", AuthMode: WebhookAuthHMAC,
			Secret: "k", SignHeader: "X-Hub-Signature-256"},
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"x":1}`)
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/trigger/d", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestWebhook_HMAC_WrongSignature(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/e", Template: "tpl_a", AuthMode: WebhookAuthHMAC,
			Secret: "k", SignHeader: "X-Hub-Signature-256"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/trigger/e", bytes.NewBufferString(`{"x":1}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestWebhook_UnknownTemplate(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/f", Template: "missing", AuthMode: WebhookAuthNone},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/trigger/f", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestWebhook_InvalidJSONBody(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/g", Template: "tpl_a", AuthMode: WebhookAuthNone},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/trigger/g", bytes.NewBufferString("not-json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestWebhook_BodyTooLarge(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/h", Template: "tpl_a", AuthMode: WebhookAuthNone},
	}); err != nil {
		t.Fatal(err)
	}

	huge := make([]byte, webhookMaxBodyBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	req := httptest.NewRequest(http.MethodPost, "/trigger/h", bytes.NewReader(huge))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func TestWebhook_GET_Rejected(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	if err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/trigger/i", Template: "tpl_a", AuthMode: WebhookAuthNone},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/trigger/i", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestWebhook_DuplicatePathRejected(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	err := engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/dup", Template: "tpl_a", AuthMode: WebhookAuthNone},
		{Path: "/dup", Template: "tpl_a", AuthMode: WebhookAuthNone},
	})
	if err == nil {
		t.Error("expected error for duplicate path")
	}
}

func TestWebhook_AuthValidationErrors(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	cases := []struct {
		name    string
		trigger WebhookTrigger
	}{
		{"bearer no secret", WebhookTrigger{Path: "/x", Template: "tpl_a", AuthMode: WebhookAuthBearer}},
		{"hmac no secret", WebhookTrigger{Path: "/y", Template: "tpl_a", AuthMode: WebhookAuthHMAC, SignHeader: "X-Sig"}},
		{"hmac no header", WebhookTrigger{Path: "/z", Template: "tpl_a", AuthMode: WebhookAuthHMAC, Secret: "k"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := engine.RegisterWebhooks(http.NewServeMux(), runtime, []WebhookTrigger{c.trigger})
			if err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestWebhook_WorkflowActuallyStarts(t *testing.T) {
	t.Parallel()
	engine, runtime, _ := noopWebhookEngine(t)
	mux := http.NewServeMux()
	_ = engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{
		{Path: "/end-to-end", Template: "tpl_a", AuthMode: WebhookAuthNone},
	})

	req := httptest.NewRequest(http.MethodPost, "/end-to-end", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	wfID := resp["workflow_id"]

	// Wait briefly for async start to settle and the workflow to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		loaded, ok := engine.store.Load(wfID)
		if ok && loaded.IsTerminal() {
			if loaded.State != StateCompleted {
				t.Errorf("workflow state = %s, want completed", loaded.State)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workflow %s did not complete within deadline", wfID)
}
