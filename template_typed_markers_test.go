package workflow

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestInstantiateStep_TypedIntMarker(t *testing.T) {
	t.Parallel()
	ts := TemplateStep{
		ID:     "test_step",
		Kind:   "tool",
		Config: json.RawMessage(`{"tool":"x","input":{"wait_ms":"@@int:delay"}}`),
	}
	merged := map[string]any{"delay": "5000"}
	s, err := instantiateStep(ts, merged)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.Config)
	var gotMap map[string]any
	if err := json.Unmarshal(raw, &gotMap); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	inputVal, ok := gotMap["input"].(map[string]any)
	if !ok {
		t.Fatalf("input not a map: %T", gotMap["input"])
	}
	if _, ok := inputVal["wait_ms"].(float64); !ok {
		t.Errorf("wait_ms not numeric: got %T = %v", inputVal["wait_ms"], inputVal["wait_ms"])
	}
}

func TestInstantiateStep_TypedBoolMarker(t *testing.T) {
	t.Parallel()
	ts := TemplateStep{
		ID:     "test_step",
		Kind:   "tool",
		Config: json.RawMessage(`{"input":{"flag":"@@bool:on"}}`),
	}
	merged := map[string]any{"on": "true"}
	s, err := instantiateStep(ts, merged)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.Config)
	if !bytes.Contains(raw, []byte(`"flag":true`)) {
		t.Errorf("expected flag:true in %s", raw)
	}
}

func TestInstantiateStep_PreserveStringClassic(t *testing.T) {
	t.Parallel()
	// Classic {{x}} substitution must still work after the typed-marker fix.
	ts := TemplateStep{
		ID:     "test_step",
		Kind:   "tool",
		Config: json.RawMessage(`{"input":{"name":"{{user}}"}}`),
	}
	merged := map[string]any{"user": "alice"}
	s, err := instantiateStep(ts, merged)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.Config)
	if !bytes.Contains(raw, []byte(`"name":"alice"`)) {
		t.Errorf("expected name:alice in %s", raw)
	}
}
