package workflow

import (
	"strings"
	"testing"
)

// TestEngine_ValidateTemplate_NilTemplate asserts the convenience nil-pass.
func TestEngine_ValidateTemplate_NilTemplate(t *testing.T) {
	e := NewEngine(newTestStore(t), WithToolRunner(&mockToolRunner{}))
	if err := e.ValidateTemplate(nil); err != nil {
		t.Errorf("nil template should pass, got %v", err)
	}
}

// TestEngine_ValidateTemplate_AllRegistered passes when every step kind in
// the template has an executor on the engine. Tool executor is registered by
// default in NewEngine.
func TestEngine_ValidateTemplate_AllRegistered(t *testing.T) {
	e := NewEngine(newTestStore(t), WithToolRunner(&mockToolRunner{}))
	tmpl := &Template{
		Name: "tool-only",
		Steps: []TemplateStep{
			{ID: "a", Kind: StepTool, Config: []byte(`{}`)},
		},
	}
	if err := e.ValidateTemplate(tmpl); err != nil {
		t.Errorf("tool-only template should pass with default executors, got %v", err)
	}
}

// TestEngine_ValidateTemplate_MissingVision is the regression guard for the
// 2026-04-25 dark_pattern_detector smoke. An engine with no LLM/vision
// provider must reject a template with a vision step at validation time —
// not at run time with the cryptic "no executor for step kind" error.
func TestEngine_ValidateTemplate_MissingVision(t *testing.T) {
	// Tool runner is wired so the FIRST missing kind reported is "vision",
	// not "tool" — the test asserts the provider hint specifically for vision.
	e := NewEngine(newTestStore(t), WithToolRunner(&mockToolRunner{}))
	tmpl := &Template{
		Name: "needs-vision",
		Steps: []TemplateStep{
			{ID: "render", Kind: StepTool, Config: []byte(`{}`)},
			{ID: "analyze", Kind: StepVision, Config: []byte(`{}`)},
		},
	}
	err := e.ValidateTemplate(tmpl)
	if err == nil {
		t.Fatal("expected error for missing vision executor, got nil")
	}
	if !strings.Contains(err.Error(), "vision") {
		t.Errorf("error should mention vision kind: %v", err)
	}
	if !strings.Contains(err.Error(), "WithLLMProvider") && !strings.Contains(err.Error(), "WithVisionProvider") {
		t.Errorf("error should hint at provider option: %v", err)
	}
	if !strings.Contains(err.Error(), "needs-vision") {
		t.Errorf("error should name the template: %v", err)
	}
}

// TestEngine_ValidateTemplate_NormalizeAlias confirms aliased step kinds
// (e.g. "if" → condition, "http_request" → tool) don't false-error.
func TestEngine_ValidateTemplate_NormalizeAlias(t *testing.T) {
	e := NewEngine(newTestStore(t), WithToolRunner(&mockToolRunner{}))
	tmpl := &Template{
		Name: "aliased",
		Steps: []TemplateStep{
			{ID: "fetch", Kind: "http_request", Config: []byte(`{"tool": "x"}`)},
		},
	}
	if err := e.ValidateTemplate(tmpl); err != nil {
		t.Errorf("http_request alias should normalize to tool, got %v", err)
	}
}
