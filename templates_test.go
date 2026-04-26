package workflow

import (
	"strings"
	"testing"
)

// TestParseTemplate_DependsAlias verifies the n8n-style "depends" key is
// transparently rewritten to "depends_on" so existing templates continue to
// load. Without this, encoding/json silently drops the unknown field and the
// step ends up with no DependsOn — a class of silent failure that previously
// shipped to prod undetected.
func TestParseTemplate_DependsAlias(t *testing.T) {
	data := []byte(`{
		"name": "alias-test",
		"steps": [
			{"id": "a", "kind": "tool", "config": {}},
			{"id": "b", "kind": "tool", "config": {}, "depends": ["a"]}
		]
	}`)
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tmpl.Steps) != 2 {
		t.Fatalf("steps: got %d", len(tmpl.Steps))
	}
	if got := tmpl.Steps[1].DependsOn; len(got) != 1 || got[0] != "a" {
		t.Errorf("Steps[1].DependsOn = %v, want [a]", got)
	}
}

// TestParseTemplate_CanonicalWinsOverAlias documents the conflict-resolution
// rule: if a step has both depends_on and depends, the canonical wins and the
// alias is dropped (the alias would have been silently dropped anyway under
// strict unmarshal — this is the explicit-but-still-permissive behavior).
func TestParseTemplate_CanonicalWinsOverAlias(t *testing.T) {
	data := []byte(`{
		"name": "conflict",
		"steps": [
			{"id": "a", "kind": "tool", "config": {}},
			{"id": "b", "kind": "tool", "config": {}, "depends": ["x"], "depends_on": ["a"]}
		]
	}`)
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := tmpl.Steps[1].DependsOn; len(got) != 1 || got[0] != "a" {
		t.Errorf("DependsOn = %v, want canonical [a]", got)
	}
}

// TestParseTemplate_RejectsUnknownStepField guarantees the strict-unmarshal
// behavior: a typo in a step field (e.g. "dependz_on") is no longer dropped
// silently — the loader fails with an error mentioning the bad field.
func TestParseTemplate_RejectsUnknownStepField(t *testing.T) {
	data := []byte(`{
		"name": "typo",
		"steps": [
			{"id": "a", "kind": "tool", "config": {}, "dependz_on": ["x"]}
		]
	}`)
	_, err := ParseTemplate(data)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "dependz_on") {
		t.Errorf("error should mention bad field, got: %v", err)
	}
}

// TestParseTemplate_RejectsUnknownTopLevelField extends strictness up to the
// Template root — guards against mis-spelled "defaultes", "stepss" etc.
func TestParseTemplate_RejectsUnknownTopLevelField(t *testing.T) {
	data := []byte(`{
		"name": "typo",
		"steps": [],
		"defaultes": {}
	}`)
	_, err := ParseTemplate(data)
	if err == nil || !strings.Contains(err.Error(), "defaultes") {
		t.Errorf("expected unknown-field error mentioning defaultes, got: %v", err)
	}
}

// TestParseTemplate_NoSteps is a regression guard: rewriteStepAliases handles
// the steps-missing case (or non-array) without crashing.
func TestParseTemplate_NoSteps(t *testing.T) {
	data := []byte(`{"name": "empty"}`)
	tmpl, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tmpl.Steps) != 0 {
		t.Errorf("steps: got %d, want 0", len(tmpl.Steps))
	}
}
