package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// VisionExecutor implements StepExecutor for StepVision. It calls a multimodal
// LLM provider with a text prompt + N image attachments. Image inputs may be
// raw file paths or "$steps.X.path" references to outputs of upstream steps
// (typically a StepImage render).
//
// When the registered provider does not implement VisionCapable (or returns
// false), the executor logs a warning and falls back to a text-only call so
// templates remain portable across providers.
//
// When step config carries a "schema" hint, the executor attempts to parse the
// LLM response as JSON. On parse failure it retries the call once with a
// stricter system message; if parsing still fails it succeeds the step with
// parsed=nil (conservative — schema mismatch is logged, not fatal).
// MIME type constants for image content.
const (
	mimePNG    = "image/png"
	mimeJPEG   = "image/jpeg"
	mimeWebP   = "image/webp"
	mimeGIF    = "image/gif"
	mimeOctet  = "application/octet-stream"
)

type VisionExecutor struct {
	provider LLMProvider
	metrics  *Metrics
}

// NewVisionExecutor builds a VisionExecutor wired to the given provider + metrics.
// metrics may be nil; counter writes are guarded.
func NewVisionExecutor(p LLMProvider, metrics *Metrics) *VisionExecutor {
	return &VisionExecutor{provider: p, metrics: metrics}
}

// Execute resolves image refs, builds a multimodal LLMMessage, calls the
// provider, and stores the response in step.Result + wf.Context.
func (e *VisionExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	if e.provider == nil {
		e.recordFailure()
		return fmt.Errorf("vision step %s: no LLMProvider registered", step.ID)
	}

	prompt, err := e.resolvePrompt(step, wf)
	if err != nil {
		e.recordFailure()
		return fmt.Errorf("vision step %s: %w", step.ID, err)
	}

	images, err := e.resolveImages(step, wf)
	if err != nil {
		e.recordFailure()
		return fmt.Errorf("vision step %s: %w", step.ID, err)
	}

	model, _ := step.Config["model"].(string)
	if model == "" {
		model = e.provider.GetDefaultModel()
	}

	supportsVision := false
	if vc, ok := e.provider.(VisionCapable); ok && vc.SupportsVision() {
		supportsVision = true
	}

	msg := LLMMessage{Role: "user", Content: prompt}
	if supportsVision {
		msg.Images = images
	} else if len(images) > 0 {
		slog.Warn("vision step degraded to text-only",
			"step_id", step.ID,
			"image_count", len(images),
			"reason", "provider does not implement VisionCapable")
	}

	resp, err := e.provider.Chat(ctx, []LLMMessage{msg}, model)
	if err != nil {
		e.recordFailure()
		return fmt.Errorf("vision step %s: chat: %w", step.ID, err)
	}

	out := initVisionOutput(resp, model)

	if schema, _ := step.Config["schema"].(string); schema != "" {
		e.applySchema(ctx, out, resp, prompt, msg, model, schema, step.ID)
	}

	step.Result = out
	wf.Context[step.ID] = out

	tokensIn, _ := out["tokens_in"].(int)
	tokensOut, _ := out["tokens_out"].(int)
	e.recordSuccess(int64(tokensIn), int64(tokensOut))
	return nil
}

// initVisionOutput builds the initial step output map from the LLM response.
func initVisionOutput(resp *LLMResponse, model string) map[string]any {
	out := map[string]any{
		"text":       resp.Content,
		"model":      resp.Model,
		"tokens_in":  resp.InputTokens,
		"tokens_out": resp.OutputTokens,
		"parsed":     nil,
	}
	if out["model"] == "" {
		out["model"] = model
	}
	return out
}

// applySchema runs JSON validation + optional retry, mutating out in place.
func (e *VisionExecutor) applySchema(
	ctx context.Context,
	out map[string]any,
	resp *LLMResponse,
	prompt string,
	msg LLMMessage,
	model, schema, stepID string,
) {
	parsed, retryResp, err := e.parseAndMaybeRetry(ctx, resp.Content, prompt, msg, model, schema)
	if err == nil && parsed != nil {
		out["parsed"] = parsed
		mergeRetryTokens(out, resp, retryResp)
		if retryResp != nil {
			out["text"] = retryResp.Content
			if retryResp.Model != "" {
				out["model"] = retryResp.Model
			}
		}
		return
	}
	slog.Warn("vision step schema validation failed", "step_id", stepID, "err", err)
	mergeRetryTokens(out, resp, retryResp)
}

// mergeRetryTokens sums first-call + retry-call token counts into out.
func mergeRetryTokens(out map[string]any, first *LLMResponse, retry *LLMResponse) {
	if retry == nil {
		return
	}
	out["tokens_in"] = first.InputTokens + retry.InputTokens
	out["tokens_out"] = first.OutputTokens + retry.OutputTokens
}

// resolvePrompt extracts and resolves the prompt config field.
func (e *VisionExecutor) resolvePrompt(step *Step, wf *Workflow) (string, error) {
	raw, _ := step.Config["prompt"].(string)
	if raw == "" {
		return "", errors.New("missing prompt")
	}
	return resolvePromptRefs(raw, wf), nil
}

// resolveImages turns image_refs (or image_paths) into LLMImageContent slices.
// Each ref may be a raw filesystem path or a "$steps.X.field" reference whose
// resolved value is a string path or []byte payload.
func (e *VisionExecutor) resolveImages(step *Step, wf *Workflow) ([]LLMImageContent, error) {
	rawRefs := stringSliceFromConfig(step.Config["image_refs"])
	if len(rawRefs) == 0 {
		rawRefs = stringSliceFromConfig(step.Config["image_paths"])
	}
	if len(rawRefs) == 0 {
		return nil, nil
	}

	out := make([]LLMImageContent, 0, len(rawRefs))
	for _, ref := range rawRefs {
		resolved := resolveRef(ref, wf)
		switch v := resolved.(type) {
		case string:
			img, err := readImageFile(v)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		case []byte:
			out = append(out, LLMImageContent{Bytes: v, MIMEType: detectMIMEFromBytes(v)})
		default:
			return nil, fmt.Errorf("image ref %q resolved to unsupported type %T", ref, resolved)
		}
	}
	return out, nil
}

// readImageFile reads a file from disk and detects MIME by extension.
func readImageFile(path string) (LLMImageContent, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from workflow config controlled by template author
	if err != nil {
		return LLMImageContent{}, fmt.Errorf("read image %q: %w", path, err)
	}
	return LLMImageContent{
		Bytes:    data,
		MIMEType: mimeFromExt(path),
	}, nil
}

// mimeFromExt maps a file extension to an image MIME type.
func mimeFromExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return mimePNG
	case ".jpg", ".jpeg":
		return mimeJPEG
	case ".webp":
		return mimeWebP
	case ".gif":
		return mimeGIF
	}
	return mimeOctet
}

// detectMIMEFromBytes does a lightweight magic-byte sniff. Returns
// application/octet-stream when nothing matches.
func detectMIMEFromBytes(b []byte) string {
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return mimePNG
	}
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return mimeJPEG
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return mimeWebP
	}
	return mimeOctet
}

// parseAndMaybeRetry attempts to JSON-parse the response. If unmarshal fails,
// it issues one retry with a system-style suffix asking for valid JSON.
// Returns (parsed, retryResp, err).
//   - parsed != nil → success on first or second try; retryResp non-nil only when retried
//   - parsed == nil + err != nil → both attempts failed; caller should warn
func (e *VisionExecutor) parseAndMaybeRetry(
	ctx context.Context,
	content, prompt string,
	original LLMMessage,
	model, schema string,
) (any, *LLMResponse, error) {
	var parsed any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &parsed); err == nil {
		return parsed, nil, nil
	}

	// Retry: re-issue the call with a stricter prompt.
	retryPrompt := prompt + "\n\nYour previous response was not valid JSON conforming to the schema:\n" +
		schema + "\nOutput ONLY valid JSON with no surrounding prose."
	retryMsg := LLMMessage{Role: "user", Content: retryPrompt, Images: original.Images}
	resp, err := e.provider.Chat(ctx, []LLMMessage{retryMsg}, model)
	if err != nil {
		return nil, nil, fmt.Errorf("retry chat: %w", err)
	}
	var parsed2 any
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &parsed2); err == nil {
		return parsed2, resp, nil
	} else {
		return nil, resp, fmt.Errorf("schema parse failed twice: %w", err)
	}
}

func (e *VisionExecutor) recordSuccess(tokensIn, tokensOut int64) {
	if e.metrics == nil {
		return
	}
	e.metrics.VisionCallsSuccess.Add(1)
	e.metrics.VisionTokensInput.Add(tokensIn)
	e.metrics.VisionTokensOutput.Add(tokensOut)
}

func (e *VisionExecutor) recordFailure() {
	if e.metrics == nil {
		return
	}
	e.metrics.VisionCallsFailed.Add(1)
}
