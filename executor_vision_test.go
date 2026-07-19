package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeLLMProvider is an in-memory LLMProvider for vision tests. When
// `vision` is true it advertises VisionCapable+SupportsVision; the
// non-capable variant omits the method entirely (separate type).
type fakeLLMProvider struct {
	mu           sync.Mutex
	defaultModel string
	responses    []LLMResponse // canned responses, consumed left-to-right
	err          error
	calls        []fakeLLMCall
}

type fakeLLMCall struct {
	Messages []LLMMessage
	Model    string
}

func (f *fakeLLMProvider) Chat(_ context.Context, msgs []LLMMessage, model string) (*LLMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Defensive copy of messages to capture image bytes at call time.
	cp := make([]LLMMessage, len(msgs))
	for i, m := range msgs {
		cp[i] = LLMMessage{Role: m.Role, Content: m.Content}
		if len(m.Images) > 0 {
			cp[i].Images = make([]LLMImageContent, len(m.Images))
			for j, img := range m.Images {
				bytesCopy := make([]byte, len(img.Bytes))
				copy(bytesCopy, img.Bytes)
				cp[i].Images[j] = LLMImageContent{Bytes: bytesCopy, MIMEType: img.MIMEType}
			}
		}
	}
	f.calls = append(f.calls, fakeLLMCall{Messages: cp, Model: model})
	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) == 0 {
		return &LLMResponse{Content: "", Model: model}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	if resp.Model == "" {
		resp.Model = model
	}
	return &resp, nil
}

func (f *fakeLLMProvider) GetDefaultModel() string {
	if f.defaultModel == "" {
		return "fake-default"
	}
	return f.defaultModel
}

func (f *fakeLLMProvider) lastCall() fakeLLMCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeLLMCall{}
	}
	return f.calls[len(f.calls)-1]
}

// fakeVisionProvider is fakeLLMProvider + VisionCapable.
type fakeVisionProvider struct{ fakeLLMProvider }

func (f *fakeVisionProvider) SupportsVision() bool { return true }

func newVisionWorkflow(stepCfg map[string]any) (*Workflow, *Step) {
	step := &Step{ID: "curator", Kind: StepVision, Config: stepCfg}
	wf := NewWorkflow("wf-vis", "vision", "telegram:1", []Step{*step})
	return wf, &wf.Steps[0]
}

// 1. Text-only fallback: provider doesn't implement VisionCapable.
func TestVisionExecutor_TextOnlyFallback(t *testing.T) {
	t.Parallel()
	p := &fakeLLMProvider{
		responses: []LLMResponse{{Content: "ok", Model: "m1", InputTokens: 3, OutputTokens: 2}},
	}
	ex := NewVisionExecutor(p, NewMetrics())

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "x.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\nXX"), 0o600); err != nil {
		t.Fatal(err)
	}

	wf, step := newVisionWorkflow(map[string]any{
		"prompt":     "describe",
		"image_refs": []any{imgPath},
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	call := p.lastCall()
	if len(call.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(call.Messages))
	}
	if len(call.Messages[0].Images) != 0 {
		t.Errorf("expected text-only fallback (no images), got %d", len(call.Messages[0].Images))
	}
	out := step.Result.(map[string]any)
	if out["text"] != "ok" {
		t.Errorf("text = %v, want ok", out["text"])
	}
}

// 2. Happy path with vision-capable provider.
func TestVisionExecutor_HappyPathVisionCapable(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{{Content: "looks good", Model: "claude-opus", InputTokens: 100, OutputTokens: 5}},
	}}
	m := NewMetrics()
	ex := NewVisionExecutor(p, m)

	wf, step := newVisionWorkflow(map[string]any{
		"prompt": "review",
		"model":  "claude-opus",
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := step.Result.(map[string]any)
	if out["text"] != "looks good" {
		t.Errorf("text = %v", out["text"])
	}
	if out["model"] != "claude-opus" {
		t.Errorf("model = %v", out["model"])
	}
	if out["tokens_in"] != 100 || out["tokens_out"] != 5 {
		t.Errorf("tokens = %v/%v", out["tokens_in"], out["tokens_out"])
	}
}

// 3. Image refs from disk.
func TestVisionExecutor_ImageRefsFromDisk(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{{Content: "ok"}},
	}}
	ex := NewVisionExecutor(p, NewMetrics())

	dir := t.TempDir()
	pngPath := filepath.Join(dir, "a.png")
	pngBytes := []byte("\x89PNG\r\n\x1a\nXX")
	if err := os.WriteFile(pngPath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	jpgPath := filepath.Join(dir, "b.jpg")
	if err := os.WriteFile(jpgPath, []byte{0xFF, 0xD8, 0xFF, 0xE0}, 0o600); err != nil {
		t.Fatal(err)
	}

	wf, step := newVisionWorkflow(map[string]any{
		"prompt":     "review",
		"image_refs": []any{pngPath, jpgPath},
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	imgs := p.lastCall().Messages[0].Images
	if len(imgs) != 2 {
		t.Fatalf("want 2 images, got %d", len(imgs))
	}
	if imgs[0].MIMEType != "image/png" {
		t.Errorf("png mime = %q", imgs[0].MIMEType)
	}
	if string(imgs[0].Bytes) != string(pngBytes) {
		t.Errorf("png bytes mismatch")
	}
	if imgs[1].MIMEType != "image/jpeg" {
		t.Errorf("jpeg mime = %q", imgs[1].MIMEType)
	}
}

// 4. Image refs resolved from $steps.
func TestVisionExecutor_ImageRefsFromSteps(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{{Content: "ok"}},
	}}
	ex := NewVisionExecutor(p, NewMetrics())

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "render.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\nDATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	wf, step := newVisionWorkflow(map[string]any{
		"prompt":     "look",
		"image_refs": []any{"$steps.render.path"},
	})
	wf.Context["render"] = map[string]any{"path": imgPath}

	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	imgs := p.lastCall().Messages[0].Images
	if len(imgs) != 1 {
		t.Fatalf("want 1 image from $steps ref, got %d", len(imgs))
	}
	if imgs[0].MIMEType != "image/png" {
		t.Errorf("mime = %q", imgs[0].MIMEType)
	}
}

// 5. Missing prompt fails the step.
func TestVisionExecutor_MissingPrompt(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{}
	m := NewMetrics()
	ex := NewVisionExecutor(p, m)

	wf, step := newVisionWorkflow(map[string]any{})
	err := ex.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "missing prompt") {
		t.Fatalf("expected missing-prompt err, got %v", err)
	}
	if m.VisionCallsFailed.Load() != 1 {
		t.Errorf("VisionCallsFailed = %d, want 1", m.VisionCallsFailed.Load())
	}
}

// 6. Empty image_refs is allowed (vision call with no images).
func TestVisionExecutor_EmptyImageRefs(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{{Content: "ok"}},
	}}
	ex := NewVisionExecutor(p, NewMetrics())

	wf, step := newVisionWorkflow(map[string]any{"prompt": "hi"})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(p.lastCall().Messages[0].Images) != 0 {
		t.Errorf("expected no images")
	}
}

// 7. Image file doesn't exist.
func TestVisionExecutor_MissingImageFile(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{}
	m := NewMetrics()
	ex := NewVisionExecutor(p, m)

	wf, step := newVisionWorkflow(map[string]any{
		"prompt":     "look",
		"image_refs": []any{"/nonexistent/path/foo.png"},
	})
	err := ex.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "read image") {
		t.Fatalf("expected read error, got %v", err)
	}
	if m.VisionCallsFailed.Load() != 1 {
		t.Errorf("VisionCallsFailed = %d", m.VisionCallsFailed.Load())
	}
}

// 8. Schema validation: valid JSON parsed.
func TestVisionExecutor_SchemaValidationSuccess(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{{Content: `{"approved": true, "issues": []}`}},
	}}
	ex := NewVisionExecutor(p, NewMetrics())

	wf, step := newVisionWorkflow(map[string]any{
		"prompt": "review",
		"schema": "{approved: bool, issues: [...]}",
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := step.Result.(map[string]any)
	parsed, ok := out["parsed"].(map[string]any)
	if !ok {
		t.Fatalf("parsed not a map: %T", out["parsed"])
	}
	if parsed["approved"] != true {
		t.Errorf("parsed.approved = %v, want true", parsed["approved"])
	}
	if len(p.calls) != 1 {
		t.Errorf("expected 1 call, got %d (no retry needed)", len(p.calls))
	}
}

// 9. Schema validation: retry once on bad JSON, succeed on retry.
func TestVisionExecutor_SchemaValidationRetry(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{
			{Content: "not json at all", InputTokens: 10, OutputTokens: 4},
			{Content: `{"ok": 1}`, InputTokens: 12, OutputTokens: 3},
		},
	}}
	ex := NewVisionExecutor(p, NewMetrics())

	wf, step := newVisionWorkflow(map[string]any{
		"prompt": "give json",
		"schema": "{ok: int}",
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(p.calls) != 2 {
		t.Errorf("expected 2 calls (retry), got %d", len(p.calls))
	}
	out := step.Result.(map[string]any)
	parsed, ok := out["parsed"].(map[string]any)
	if !ok || parsed["ok"] == nil {
		t.Fatalf("parsed missing on retry: %v", out["parsed"])
	}
	if out["text"] != `{"ok": 1}` {
		t.Errorf("text = %q, want retry response", out["text"])
	}
	if out["tokens_in"] != 22 {
		t.Errorf("tokens_in = %v, want 22 (sum of both calls)", out["tokens_in"])
	}
}

// 10. Schema validation: persistent failure → success with parsed=nil.
func TestVisionExecutor_SchemaValidationPersistentFailure(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{
			{Content: "bad 1"},
			{Content: "still bad"},
		},
	}}
	ex := NewVisionExecutor(p, NewMetrics())

	wf, step := newVisionWorkflow(map[string]any{
		"prompt": "json please",
		"schema": "{x: int}",
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("step should succeed even on schema fail, got %v", err)
	}
	out := step.Result.(map[string]any)
	if out["parsed"] != nil {
		t.Errorf("parsed = %v, want nil on persistent failure", out["parsed"])
	}
	if out["text"] == "" {
		t.Error("text should still be populated")
	}
}

// 11. Provider returns error.
func TestVisionExecutor_ProviderError(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{err: errors.New("boom")}}
	m := NewMetrics()
	ex := NewVisionExecutor(p, m)

	wf, step := newVisionWorkflow(map[string]any{"prompt": "x"})
	err := ex.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected wrapped boom, got %v", err)
	}
	if m.VisionCallsFailed.Load() != 1 {
		t.Errorf("VisionCallsFailed = %d", m.VisionCallsFailed.Load())
	}
}

// 12. Metrics happy path increments success + tokens.
func TestVisionExecutor_MetricsIncrement(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		responses: []LLMResponse{{Content: "ok", InputTokens: 50, OutputTokens: 7}},
	}}
	m := NewMetrics()
	ex := NewVisionExecutor(p, m)

	wf, step := newVisionWorkflow(map[string]any{"prompt": "x"})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if m.VisionCallsSuccess.Load() != 1 {
		t.Errorf("Success = %d", m.VisionCallsSuccess.Load())
	}
	if m.VisionTokensInput.Load() != 50 {
		t.Errorf("TokensIn = %d", m.VisionTokensInput.Load())
	}
	if m.VisionTokensOutput.Load() != 7 {
		t.Errorf("TokensOut = %d", m.VisionTokensOutput.Load())
	}
}

// Engine wiring: WithVisionProvider registers an executor.
func TestVisionExecutor_EngineWiring(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{}
	store := newTestStore(t)
	eng := NewEngine(store, WithVisionProvider(p))
	ex, ok := eng.executors[StepVision].(*VisionExecutor)
	if !ok {
		t.Fatal("StepVision not registered")
	}
	if ex.metrics == nil {
		t.Error("metrics not wired by NewEngine")
	}
}

// Engine wiring: WithLLMProvider auto-registers vision when provider is VisionCapable.
func TestVisionExecutor_AutoWireFromLLMProvider(t *testing.T) {
	t.Parallel()
	p := &fakeVisionProvider{}
	store := newTestStore(t)
	eng := NewEngine(store, WithLLMProvider(p))
	if _, ok := eng.executors[StepVision].(*VisionExecutor); !ok {
		t.Fatal("WithLLMProvider should auto-wire vision when provider is VisionCapable")
	}
}

// Engine wiring: WithLLMProvider does NOT register vision for text-only providers.
func TestVisionExecutor_NoAutoWireForTextOnly(t *testing.T) {
	t.Parallel()
	p := &fakeLLMProvider{}
	store := newTestStore(t)
	eng := NewEngine(store, WithLLMProvider(p))
	if _, ok := eng.executors[StepVision]; ok {
		t.Fatal("text-only LLMProvider should not auto-register vision")
	}
}

func TestNormalizeStepKind_VisionAliases(t *testing.T) {
	t.Parallel()
	for _, alias := range []StepKind{"llm_vision", "multimodal"} {
		if got := NormalizeStepKind(alias); got != StepVision {
			t.Errorf("NormalizeStepKind(%q) = %q, want %q", alias, got, StepVision)
		}
	}
	if !IsValidStepKind(StepVision) {
		t.Error("IsValidStepKind(StepVision) = false")
	}
}
