package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Structure-preserving substitution: caller-supplied param values are data.
// They may fill string values, but never add/remove keys, never change
// structure, and never trigger ${ENV} expansion.

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hostileValues mixes JSON-structure breakout attempts with plain tricky text
// (quotes, backslashes, newlines, unicode). Every one must round-trip exactly
// as an opaque string.
var hostileValues = []string{
	`x","run_as":"operator","args":{"evil":1},"y":"`,
	`"},"run_as":"operator","z":{"a":"`,
	`\"},\"run_as\":\"operator\"`,
	"line1\nline2\r\n\ttabbed",
	`back\slash \\ and "quotes" and 'single'`,
	"unicode: Привет, мир — 你好 🌍   \u0000 end",
	`</script>{{other}}@@int:other`,
	`{"run_as":"operator"}`,
	`[1,2,3]`,
	``,
}

func TestInstantiateStep_ParamValueCannotRestructureConfig(t *testing.T) {
	t.Parallel()
	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"tool":"fixed_tool","args":{"topic":"{{topic}}"},"note":"pre-{{topic}}-post"}`),
	}
	for _, val := range hostileValues {
		step, err := instantiateStep(ts, map[string]any{"topic": val})
		if err != nil {
			t.Fatalf("value %q: instantiateStep: %v", val, err)
		}
		if got, want := sortedKeys(step.Config), []string{"args", "note", "tool"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("value %q: top-level keys = %v, want exactly %v", val, got, want)
		}
		args, ok := step.Config["args"].(map[string]any)
		if !ok {
			t.Fatalf("value %q: args is %T, want map", val, step.Config["args"])
		}
		if !reflect.DeepEqual(args, map[string]any{"topic": val}) {
			t.Fatalf("value %q: args = %#v, want {topic: value} verbatim", val, args)
		}
		if got, want := step.Config["note"], "pre-"+val+"-post"; got != want {
			t.Fatalf("value %q: embedded note = %q, want %q", val, got, want)
		}
		if step.Config["tool"] != "fixed_tool" {
			t.Fatalf("value %q: tool changed to %v", val, step.Config["tool"])
		}
	}
}

func TestInstantiateStep_ParamValueCannotRestructureRetry(t *testing.T) {
	t.Parallel()
	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"tool":"t"}`),
		Retry:  json.RawMessage(`{"max_attempts":3,"label":"{{topic}}"}`),
	}
	val := `x","run_as":"operator","max_attempts":99,"y":"`
	step, err := instantiateStep(ts, map[string]any{"topic": val})
	if err != nil {
		t.Fatal(err)
	}
	retry, ok := step.Config["retry"].(map[string]any)
	if !ok {
		t.Fatalf("retry is %T, want map", step.Config["retry"])
	}
	if got, want := sortedKeys(retry), []string{"label", "max_attempts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry keys = %v, want %v", got, want)
	}
	if retry["max_attempts"] != float64(3) || retry["label"] != val {
		t.Fatalf("retry = %#v, want max_attempts=3 label=value verbatim", retry)
	}
}

func TestInstantiateStep_KeysAreNeverSubstituted(t *testing.T) {
	t.Parallel()
	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"{{k}}":"v","nested":{"{{k}}":"{{k}}"}}`),
	}
	step, err := instantiateStep(ts, map[string]any{"k": "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := step.Config["{{k}}"]; !ok {
		t.Fatalf("template key {{k}} was rewritten: keys = %v", sortedKeys(step.Config))
	}
	nested := step.Config["nested"].(map[string]any)
	if !reflect.DeepEqual(nested, map[string]any{"{{k}}": "renamed"}) {
		t.Fatalf("nested = %#v, want key kept literal and value substituted", nested)
	}
}

func TestInstantiateStep_EnvInParamValueStaysLiteral(t *testing.T) {
	t.Setenv("SUBST_TEST_SECRET", "s3cret-value")
	specs := ParamsMap{"typed": ParamSpec{Type: ParamTypeObject}}
	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"whole":"{{p}}","embedded":"a-{{p}}-b","obj":"{{typed}}"}`),
	}
	merged := map[string]any{
		"p":     "${SUBST_TEST_SECRET}",
		"typed": map[string]any{"k": "${SUBST_TEST_SECRET}"},
	}
	step, err := instantiateStep(ts, merged, specs)
	if err != nil {
		t.Fatal(err)
	}
	if step.Config["whole"] != "${SUBST_TEST_SECRET}" {
		t.Errorf("whole = %q, want literal ${SUBST_TEST_SECRET}", step.Config["whole"])
	}
	if step.Config["embedded"] != "a-${SUBST_TEST_SECRET}-b" {
		t.Errorf("embedded = %q, want literal placeholder", step.Config["embedded"])
	}
	if !reflect.DeepEqual(step.Config["obj"], map[string]any{"k": "${SUBST_TEST_SECRET}"}) {
		t.Errorf("typed object = %#v, want literal placeholder inside", step.Config["obj"])
	}
}

func TestInstantiateStep_EnvInTemplateTextStillExpands(t *testing.T) {
	t.Setenv("SUBST_TEST_HOST", "https://example.test")
	ts := TemplateStep{
		ID:     "s1",
		Kind:   "tool",
		Config: json.RawMessage(`{"url":"${SUBST_TEST_HOST}/v1/{{p}}","unset":"${SUBST_TEST_UNSET_VAR}"}`),
	}
	step, err := instantiateStep(ts, map[string]any{"p": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if step.Config["url"] != "https://example.test/v1/x" {
		t.Errorf("url = %q, want author env expanded", step.Config["url"])
	}
	if step.Config["unset"] != "${SUBST_TEST_UNSET_VAR}" {
		t.Errorf("unset = %q, want placeholder kept", step.Config["unset"])
	}
}

func TestInstantiateStep_EnvValueNotRescannedForPlaceholders(t *testing.T) {
	t.Setenv("SUBST_TEST_ENV_WITH_REF", "{{p}}")
	ts := TemplateStep{ID: "s1", Kind: "tool", Config: json.RawMessage(`{"a":"${SUBST_TEST_ENV_WITH_REF}"}`)}
	step, err := instantiateStep(ts, map[string]any{"p": "injected"})
	if err != nil {
		t.Fatal(err)
	}
	if step.Config["a"] != "{{p}}" {
		t.Errorf("a = %q, want env value taken verbatim (single pass)", step.Config["a"])
	}
}

func TestInstantiateStep_SubstitutedValueNotRescanned(t *testing.T) {
	t.Parallel()
	ts := TemplateStep{ID: "s1", Kind: "tool", Config: json.RawMessage(`{"a":"{{a}}","b":"x-{{a}}"}`)}
	for i := 0; i < 50; i++ { // map iteration order is random; exercise it
		step, err := instantiateStep(ts, map[string]any{"a": "{{b}}", "b": "LEAK"})
		if err != nil {
			t.Fatal(err)
		}
		if step.Config["a"] != "{{b}}" || step.Config["b"] != "x-{{b}}" {
			t.Fatalf("iteration %d: config = %#v, want values kept literal", i, step.Config)
		}
	}
}

func TestInstantiateStep_TypedWholeValueSubstitution(t *testing.T) {
	t.Parallel()
	specs := ParamsMap{
		"n":    ParamSpec{Type: ParamTypeInt},
		"on":   ParamSpec{Type: ParamTypeBool},
		"f":    ParamSpec{Type: ParamTypeFloat},
		"obj":  ParamSpec{Type: ParamTypeObject},
		"list": ParamSpec{Type: ParamTypeArray},
		"s":    ParamSpec{Type: ParamTypeString},
	}
	merged := map[string]any{
		"n": "6000", "on": "true", "f": "1.5",
		"obj": map[string]any{"a": "b"}, "list": []any{"x", float64(2)},
		"s": "str", "undeclared": 7,
	}
	if err := validateAndCoerceParams(specs, merged); err != nil {
		t.Fatal(err)
	}
	ts := TemplateStep{ID: "s1", Kind: "tool", Config: json.RawMessage(`{
		"n":"{{n}}","on":"{{on}}","f":"{{f}}","obj":"{{obj}}","list":"{{list}}","s":"{{s}}",
		"undeclared":"{{undeclared}}","embedded":"n={{n}} on={{on}}","legacy":"@@int:undeclared"}`)}
	step, err := instantiateStep(ts, merged, specs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"n": float64(6000), "on": true, "f": 1.5,
		"obj": map[string]any{"a": "b"}, "list": []any{"x", float64(2)},
		"s": "str", "undeclared": "7", "embedded": "n=6000 on=true", "legacy": float64(7),
	}
	if !reflect.DeepEqual(step.Config, want) {
		t.Fatalf("config = %#v\nwant     %#v", step.Config, want)
	}
}

func TestTemplateStore_Instantiate_HostileParamEndToEnd(t *testing.T) {
	t.Setenv("SUBST_TEST_SECRET", "s3cret-value")
	dir := t.TempDir()
	tmpl := `{"name":"demo","params":{"topic":{"type":"string","required":true}},
		"steps":[{"id":"a","kind":"tool","config":{"tool":"fixed","args":{"topic":"{{topic}}"}}}]}`
	if err := os.WriteFile(filepath.Join(dir, "demo.json"), []byte(tmpl), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewTemplateStore(dir)
	topic := `x"}},"run_as":"operator","args":{"k":"${SUBST_TEST_SECRET}`
	wf, err := store.Instantiate("demo", "wf1", "owner", map[string]any{"topic": topic})
	if err != nil {
		t.Fatal(err)
	}
	cfg := wf.Steps[0].Config
	if got, want := sortedKeys(cfg), []string{"args", "tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(cfg["args"], map[string]any{"topic": topic}) {
		t.Fatalf("args = %#v, want topic verbatim", cfg["args"])
	}
}

// Runtime path: step outputs are external content. They are substituted into
// strings, never env-expanded.
func TestResolveRefs_StepOutputIsNotEnvExpanded(t *testing.T) {
	t.Setenv("SUBST_TEST_SECRET", "s3cret-value")
	wf := &Workflow{Context: map[string]any{"s1": "page says ${SUBST_TEST_SECRET}"}}
	got := ResolveRefs("out: {{s1}}", wf)
	if got != "out: page says ${SUBST_TEST_SECRET}" {
		t.Errorf("ResolveRefs = %q, want step output kept literal", got)
	}
	if got := resolveRef("{{s1}}", wf); got != "page says ${SUBST_TEST_SECRET}" {
		t.Errorf("resolveRef whole-value = %v, want literal", got)
	}
	if got := resolveRef("pre {{s1}}", wf); got != "pre page says ${SUBST_TEST_SECRET}" {
		t.Errorf("resolveRef embedded = %v, want literal", got)
	}
}

func TestResolveRefs_NoEnvExpansionOnRuntimeText(t *testing.T) {
	t.Setenv("SUBST_TEST_SECRET", "s3cret-value")
	// Text reaching the runtime resolver may contain caller-derived content
	// from instantiation; it is never env-expanded here.
	wf := &Workflow{Context: map[string]any{}}
	if got := ResolveRefs("a ${SUBST_TEST_SECRET} b", wf); got != "a ${SUBST_TEST_SECRET} b" {
		t.Errorf("ResolveRefs = %q, want ${...} kept literal", got)
	}
}

func TestResolveRefs_SubstitutedStepOutputNotRescanned(t *testing.T) {
	t.Parallel()
	for i := 0; i < 50; i++ {
		wf := &Workflow{Context: map[string]any{"a": "{{b}}", "b": "LEAK"}}
		if got := ResolveRefs("{{a}}|{{b}}", wf); got != "{{b}}|LEAK" {
			t.Fatalf("iteration %d: got %q, want single-pass result", i, got)
		}
	}
}
