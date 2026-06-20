package workflow

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// --- Fix 1: int precision ---

// TestParamSpec_IntPreservesInt64Precision verifies that coerceParamValue for
// ParamTypeInt returns int64, not float64. float64 loses precision for integers
// > 2^53 (e.g. Telegram chat_id, Discord snowflake). Reproduces the bug where
// fmt.Sscanf("%g") silently rounded 9007199254740993 → 9007199254740992.
func TestParamSpec_IntPreservesInt64Precision(t *testing.T) {
	// 2^53 + 1 = 9007199254740993 — first integer not representable as float64.
	v, err := coerceParamValue(ParamTypeInt, "9007199254740993")
	if err != nil {
		t.Fatalf("coerceParamValue: %v", err)
	}
	got, ok := v.(int64)
	if !ok {
		t.Fatalf("returned type = %T (%v), want int64", v, v)
	}
	const want int64 = 9007199254740993
	if got != want {
		t.Errorf("got %d, want %d (precision loss via float64?)", got, want)
	}
}

// TestParamSpec_IntCoercionProducesCorrectLiteral verifies that an int64
// param (> 2^53) flows through validateAndCoerceParams + applyTypedSubstitutions
// and produces a JSON literal containing the exact integer string — not
// scientific notation, not a rounded float64 representation.
// Note: step.Config (map[string]any) re-decodes numbers via float64 per Go
// stdlib; this test verifies the intermediate JSON wire string is correct.
func TestParamSpec_IntCoercionProducesCorrectLiteral(t *testing.T) {
	specs := ParamsMap{
		"chat_id": ParamSpec{Type: ParamTypeInt, Description: "Telegram chat id"},
	}
	merged := map[string]any{"chat_id": int64(9007199254740993)}

	// Coerce: stores int64 in merged.
	if err := validateAndCoerceParams(specs, merged); err != nil {
		t.Fatalf("validateAndCoerceParams: %v", err)
	}
	v, ok := merged["chat_id"].(int64)
	if !ok {
		t.Fatalf("after coerce: type = %T, want int64", merged["chat_id"])
	}
	if v != 9007199254740993 {
		t.Errorf("after coerce: got %d, want 9007199254740993", v)
	}

	// Substitution: json.Marshal(int64) must emit bare integer literal.
	configStr := `{"tool":"send","input":{"chat_id":"{{chat_id}}"}}`
	got := applyTypedSubstitutions(configStr, merged, specs)
	if !strings.Contains(got, "9007199254740993") {
		t.Errorf("substituted config = %s; want to contain 9007199254740993 as bare integer", got)
	}
	if strings.Contains(got, "e+") || strings.Contains(got, "E+") || strings.Contains(got, "e15") {
		t.Errorf("substituted config = %s; must not contain scientific notation", got)
	}
}

// --- Fix 2: deprecation warn dedup ---

// TestParamSpec_DeprecationWarnFiresOnce verifies that when a template step
// contains two @@int: markers, instantiation logs exactly one deprecation
// warning per unique marker name, not one per occurrence per call.
func TestParamSpec_DeprecationWarnFiresOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"a":"@@int:x","b":"@@int:x"}`),
	}
	merged := map[string]any{"x": 42}

	// Call instantiateStep twice to confirm the dedup is process-wide.
	for range 2 {
		if _, err := instantiateStep(ts, merged); err != nil {
			t.Fatalf("instantiateStep: %v", err)
		}
	}

	logOutput := buf.String()
	count := strings.Count(logOutput, "deprecated")
	if count != 1 {
		t.Errorf("expected exactly 1 deprecation warn, got %d\nlog:\n%s", count, logOutput)
	}
}

// --- Fix 3: ParamsMap MarshalJSON ---

// TestParamsMap_LegacyShapeRoundTrip verifies that a trivially-legacy
// ParamSpec (Type=string, no default/required/enum) marshals as a bare
// string — the legacy wire shape — not as a full {"type":"string",...} object.
// This preserves backward-compat for downstream consumers reading
// params as map[string]string.
func TestParamsMap_LegacyShapeRoundTrip(t *testing.T) {
	pm := ParamsMap{
		"webhook_path": ParamSpec{Type: ParamTypeString, Description: "/hook/v1"},
	}
	b, err := json.Marshal(pm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `{"webhook_path":"/hook/v1"}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	// Round-trip: unmarshal back must restore the original ParamSpec.
	var pm2 ParamsMap
	if err := json.Unmarshal(b, &pm2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := pm2["webhook_path"]
	if spec.Type != ParamTypeString || spec.Description != "/hook/v1" {
		t.Errorf("round-trip: got %+v, want {Type:string, Description:/hook/v1}", spec)
	}
}

// TestParamsMap_TypedShapeStaysTyped verifies that a typed ParamSpec
// (non-string type, or with default/required/enum) marshals as the full
// {"type":"int","default":6,...} object form, not a bare string.
func TestParamsMap_TypedShapeStaysTyped(t *testing.T) {
	six := any(float64(6))
	pm := ParamsMap{
		"count": ParamSpec{Type: ParamTypeInt, Default: six, Description: "items"},
	}
	b, err := json.Marshal(pm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	// Must contain type and default as object fields.
	if !strings.Contains(got, `"type":"int"`) {
		t.Errorf("got %s; want to contain \"type\":\"int\"", got)
	}
	if !strings.Contains(got, `"default"`) {
		t.Errorf("got %s; want to contain \"default\"", got)
	}
	// Must NOT be a bare string.
	if strings.HasPrefix(strings.TrimPrefix(got, `{"count":`), `"`) {
		t.Errorf("got %s; typed spec must not marshal as a bare string", got)
	}
}

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

// TestParamSpec_SmokeTemplatesParseClean is a backward-compat smoke test that
// parses every *.json template from a local templates directory (which uses
// the legacy string-description params form and @@int markers) and verifies
// they still load without error. Skipped when WORKFLOW_SMOKE_TEMPLATES_DIR is
// not set or the directory is not present (CI or other checkout locations).
func TestParamSpec_SmokeTemplatesParseClean(t *testing.T) {
	dir := os.Getenv("WORKFLOW_SMOKE_TEMPLATES_DIR")
	if dir == "" {
		t.Skip("WORKFLOW_SMOKE_TEMPLATES_DIR not set, skipping smoke test")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("templates dir not found (%s), skipping smoke test: %v", dir, err)
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
	t.Logf("parsed %d templates cleanly", parsed)
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
