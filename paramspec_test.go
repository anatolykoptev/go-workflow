package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestParamSpec_LegacyFormParsesAsStringType verifies that the legacy
// string-only params form ("name": "Description text") is accepted and
// promoted to ParamSpec{Type:"string", Description:"Description text"}.
// This is the backward-compat invariant: existing templates must load.
func TestParamSpec_LegacyFormParsesAsStringType(t *testing.T) {
	data := []byte(`{
		"name": "legacy",
		"params": {
			"x": "description text"
		},
		"steps": []
	}`)
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, ok := tmpl.Params["x"]
	if !ok {
		t.Fatal("params[x] not found")
	}
	if spec.Type != "string" {
		t.Errorf("Type = %q, want %q", spec.Type, "string")
	}
	if spec.Description != "description text" {
		t.Errorf("Description = %q, want %q", spec.Description, "description text")
	}
}

// TestParamSpec_TypedFormParses verifies the new object form
// {"type":"int","default":6} parses into ParamSpec correctly.
func TestParamSpec_TypedFormParses(t *testing.T) {
	data := []byte(`{
		"name": "typed",
		"params": {
			"x": {"type": "int", "default": 6, "description": "wait ms"}
		},
		"steps": []
	}`)
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, ok := tmpl.Params["x"]
	if !ok {
		t.Fatal("params[x] not found")
	}
	if spec.Type != "int" {
		t.Errorf("Type = %q, want %q", spec.Type, "int")
	}
	if spec.Description != "wait ms" {
		t.Errorf("Description = %q, want %q", spec.Description, "wait ms")
	}
	// Default should be numeric 6 (JSON numbers decode as float64 via any)
	defVal, ok := spec.Default.(float64)
	if !ok {
		t.Errorf("Default type = %T, want float64 (JSON number), value = %v", spec.Default, spec.Default)
	} else if defVal != 6 {
		t.Errorf("Default = %v, want 6", defVal)
	}
}

// TestParamSpec_CoercionBeforeSubstitution verifies that a typed int param
// causes "{{wait_ms}}" surrounded by JSON quotes to be emitted as a bare
// integer in the instantiated step config — not as a quoted string.
// This is the root-cause fix for the chrome_interact wait_ms bug.
func TestParamSpec_CoercionBeforeSubstitution(t *testing.T) {
	data := []byte(`{
		"name": "coerce",
		"params": {
			"wait_ms": {"type": "int", "description": "delay", "default": 6000}
		},
		"steps": [
			{
				"id": "s1",
				"kind": "tool",
				"config": {
					"tool": "chrome_interact",
					"input": {
						"wait_ms": "{{wait_ms}}"
					}
				}
			}
		]
	}`)
	store := &TemplateStore{templates: make(map[string]*Template)}
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store.templates["coerce"] = tmpl

	wf, err := store.Instantiate("coerce", "wf-1", "test", map[string]any{"wait_ms": 6000})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	step, err := findStep(wf, "s1")
	if err != nil {
		t.Fatal(err)
	}
	input, ok := step.Config["input"].(map[string]any)
	if !ok {
		t.Fatalf("input not a map: %T %v", step.Config["input"], step.Config["input"])
	}
	// wait_ms must be a numeric type (float64 after JSON round-trip), not a string
	waitVal := input["wait_ms"]
	if _, ok := waitVal.(float64); !ok {
		t.Errorf("wait_ms type = %T (value=%v), want float64 (bare int in JSON)", waitVal, waitVal)
	}
}

// TestParamSpec_CoercionWithStringInput verifies coercion also works when
// the caller passes wait_ms as a string (e.g., from an env var or URL param).
func TestParamSpec_CoercionWithStringInput(t *testing.T) {
	data := []byte(`{
		"name": "coerce-str",
		"params": {
			"wait_ms": {"type": "int", "description": "delay"}
		},
		"steps": [
			{
				"id": "s1",
				"kind": "tool",
				"config": {"tool": "t", "input": {"wait_ms": "{{wait_ms}}"}}
			}
		]
	}`)
	store := &TemplateStore{templates: make(map[string]*Template)}
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store.templates["coerce-str"] = tmpl

	wf, err := store.Instantiate("coerce-str", "wf-2", "test", map[string]any{"wait_ms": "3000"})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	step, _ := findStep(wf, "s1")
	input := step.Config["input"].(map[string]any)
	if _, ok := input["wait_ms"].(float64); !ok {
		t.Errorf("wait_ms type = %T, want float64 after string→int coercion", input["wait_ms"])
	}
}

// TestParamSpec_MismatchedDefaultRejectedAtLoad verifies that a ParamSpec
// with type="int" but default="hello" (string) is caught at ParseTemplate,
// not at runtime. This would have caught the original `defaults["wait_ms"] = "6000"` bug.
func TestParamSpec_MismatchedDefaultRejectedAtLoad(t *testing.T) {
	data := []byte(`{
		"name": "bad-default",
		"params": {
			"x": {"type": "int", "default": "hello"}
		},
		"steps": []
	}`)
	_, err := ParseTemplate(data)
	if err == nil {
		t.Fatal("expected error for mismatched default type, got nil")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should mention param name 'x', got: %v", err)
	}
}

// TestParamSpec_MultiErrorCollection verifies that two mismatched defaults are
// both reported in a single ParseTemplate error, not just the first one.
func TestParamSpec_MultiErrorCollection(t *testing.T) {
	data := []byte(`{
		"name": "multi-bad",
		"params": {
			"a": {"type": "int", "default": "not-int"},
			"b": {"type": "bool", "default": 42}
		},
		"steps": []
	}`)
	_, err := ParseTemplate(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Both param names should appear in the error
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("expected both 'a' and 'b' in error, got: %v", err)
	}
}

// TestParamSpec_UnknownVarRejectedAtInstantiate verifies that a step config
// referencing {{unknown}} (no matching param) is rejected at Instantiate,
// not silently substituted with an empty string.
func TestParamSpec_UnknownVarRejectedAtInstantiate(t *testing.T) {
	data := []byte(`{
		"name": "unknown-var",
		"params": {
			"x": "known param"
		},
		"steps": [
			{
				"id": "s1",
				"kind": "tool",
				"config": {"tool": "t", "input": {"val": "{{unknown}}"}}
			}
		]
	}`)
	store := &TemplateStore{templates: make(map[string]*Template)}
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store.templates["unknown-var"] = tmpl

	_, err = store.Instantiate("unknown-var", "wf-3", "test", map[string]any{"x": "val"})
	if err == nil {
		t.Fatal("expected error for unknown var ref, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention 'unknown', got: %v", err)
	}
}

// TestParamSpec_LegacyAtAtMarkersStillWork verifies that @@int:NAME markers
// continue to work after introducing typed ParamSpec (deprecation warn, not fail).
// Regression guard: the @@int path must not be broken by Part 3.
func TestParamSpec_LegacyAtAtMarkersStillWork(t *testing.T) {
	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"input":{"wait_ms":"@@int:delay"}}`),
	}
	merged := map[string]any{"delay": 5000}
	step, err := instantiateStep(ts, merged)
	if err != nil {
		t.Fatalf("instantiateStep: %v", err)
	}
	raw, _ := json.Marshal(step.Config)
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	input := cfg["input"].(map[string]any)
	if _, ok := input["wait_ms"].(float64); !ok {
		t.Errorf("wait_ms type = %T (value=%v), want float64", input["wait_ms"], input["wait_ms"])
	}
}

// TestParamSpec_BoolTypedForm verifies bool-typed param coercion.
func TestParamSpec_BoolTypedForm(t *testing.T) {
	data := []byte(`{
		"name": "bool-test",
		"params": {
			"flag": {"type": "bool", "default": false}
		},
		"steps": [
			{
				"id": "s1",
				"kind": "tool",
				"config": {"tool": "t", "input": {"enabled": "{{flag}}"}}
			}
		]
	}`)
	store := &TemplateStore{templates: make(map[string]*Template)}
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store.templates["bool-test"] = tmpl

	wf, err := store.Instantiate("bool-test", "wf-4", "test", map[string]any{"flag": true})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	step, _ := findStep(wf, "s1")
	input := step.Config["input"].(map[string]any)
	if v, ok := input["enabled"].(bool); !ok || !v {
		t.Errorf("enabled = %T(%v), want bool(true)", input["enabled"], input["enabled"])
	}
}

// TestParamSpec_RequiredParamMissingAtInstantiate verifies that a required
// param not supplied at Instantiate returns an error.
func TestParamSpec_RequiredParamMissingAtInstantiate(t *testing.T) {
	data := []byte(`{
		"name": "required-test",
		"params": {
			"url": {"type": "string", "required": true, "description": "target URL"}
		},
		"steps": [
			{"id": "s1", "kind": "tool", "config": {"tool": "fetch", "input": {"url": "{{url}}"}}}
		]
	}`)
	store := &TemplateStore{templates: make(map[string]*Template)}
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store.templates["required-test"] = tmpl

	_, err = store.Instantiate("required-test", "wf-5", "test", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing required param, got nil")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should mention 'url', got: %v", err)
	}
}

// TestParamSpec_GoWowaTemplatesParseClean is a backward-compat smoke test that
// parses every *.json template from the go-wowa templates directory (which uses
// the legacy string-description params form and @@int markers) and verifies
// they still load without error. Skipped when the directory is not present
// (CI or other checkout locations).
func TestParamSpec_GoWowaTemplatesParseClean(t *testing.T) {
	dir := "/home/krolik/src/go-wowa/templates"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("go-wowa templates dir not found (%s), skipping smoke test: %v", dir, err)
	}

	parsed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".n8n.json") {
			continue
		}
		path := dir + "/" + e.Name()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", e.Name(), err)
			continue
		}
		if _, err := ParseTemplate(data); err != nil {
			t.Errorf("parse %s: %v", e.Name(), err)
		} else {
			parsed++
		}
	}
	if parsed == 0 {
		t.Error("no templates were parsed (expected at least 1)")
	}
	t.Logf("parsed %d go-wowa templates cleanly", parsed)
}

// findStep is a test helper that finds a step by ID in a workflow.
func findStep(wf *Workflow, id string) (Step, error) {
	for _, s := range wf.Steps {
		if s.ID == id {
			return s, nil
		}
	}
	return Step{}, &stepNotFoundError{id: id}
}

type stepNotFoundError struct{ id string }

func (e *stepNotFoundError) Error() string { return "step not found: " + e.id }
