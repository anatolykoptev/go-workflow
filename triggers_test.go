package workflow

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMatchesFilter_Empty(t *testing.T) {
	t.Parallel()
	if !MatchesFilter(nil, map[string]any{"a": "1"}) {
		t.Error("nil filter should match anything")
	}
	if !MatchesFilter(map[string]string{}, map[string]any{"a": "1"}) {
		t.Error("empty filter should match anything")
	}
}

func TestMatchesFilter_Match(t *testing.T) {
	t.Parallel()
	filter := map[string]string{"name": "test"}
	data := map[string]any{"name": "test", "extra": true}
	if !MatchesFilter(filter, data) {
		t.Error("should match")
	}
}

func TestMatchesFilter_CaseInsensitive(t *testing.T) {
	t.Parallel()
	filter := map[string]string{"name": "TEST"}
	data := map[string]any{"name": "test"}
	if !MatchesFilter(filter, data) {
		t.Error("should match case-insensitively")
	}
}

func TestMatchesFilter_Mismatch(t *testing.T) {
	t.Parallel()
	filter := map[string]string{"name": "other"}
	data := map[string]any{"name": "test"}
	if MatchesFilter(filter, data) {
		t.Error("should not match")
	}
}

func TestMatchesFilter_MissingKey(t *testing.T) {
	t.Parallel()
	filter := map[string]string{"missing": "val"}
	data := map[string]any{"name": "test"}
	if MatchesFilter(filter, data) {
		t.Error("should not match when key is missing")
	}
}

func TestTriggerService_AddAndEvaluate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, err := ts.AddTrigger("on-complete", EventWorkflowCompleted, nil,
		TriggerAction{Kind: "workflow", TemplateID: "tpl-1"})
	if err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	matched := ts.Evaluate(EventWorkflowCompleted, map[string]any{})
	if len(matched) != 1 {
		t.Fatalf("got %d matches, want 1", len(matched))
	}
	if matched[0].Action.TemplateID != "tpl-1" {
		t.Errorf("template_id = %q", matched[0].Action.TemplateID)
	}
}

func TestTriggerService_FilteredEvaluate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, _ = ts.AddTrigger("filtered", EventWorkflowCompleted,
		map[string]string{"workflow_name": "deploy"},
		TriggerAction{Kind: "message", Message: "done"})

	matched := ts.Evaluate(EventWorkflowCompleted, map[string]any{"workflow_name": "deploy"})
	if len(matched) != 1 {
		t.Errorf("got %d, want 1 (filter match)", len(matched))
	}

	matched = ts.Evaluate(EventWorkflowCompleted, map[string]any{"workflow_name": "other"})
	if len(matched) != 0 {
		t.Errorf("got %d, want 0 (filter mismatch)", len(matched))
	}
}

func TestTriggerService_NoMatchDifferentEvent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, _ = ts.AddTrigger("on-fail", EventWorkflowFailed, nil,
		TriggerAction{Kind: "message"})

	matched := ts.Evaluate(EventWorkflowCompleted, map[string]any{})
	if len(matched) != 0 {
		t.Errorf("got %d, want 0 (wrong event)", len(matched))
	}
}

func TestTriggerService_HookHandler(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, _ = ts.AddTrigger("hook", EventWorkflowCompleted, nil,
		TriggerAction{Kind: "workflow"})

	var executed bool
	ts.SetExecutor(func(_ *EventTrigger) error {
		executed = true
		return nil
	})

	handler := ts.HookHandler(EventWorkflowCompleted)
	_ = handler(map[string]any{})

	if !executed {
		t.Error("executor was not called")
	}
}

func TestTriggerService_HookHandler_Error(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, _ = ts.AddTrigger("err", EventWorkflowCompleted, nil,
		TriggerAction{Kind: "workflow"})

	ts.SetExecutor(func(_ *EventTrigger) error {
		return errors.New("boom")
	})

	handler := ts.HookHandler(EventWorkflowCompleted)
	err := handler(map[string]any{})
	if err != nil {
		t.Error("HookHandler should not propagate executor errors")
	}
}

func TestTriggerService_CRUD(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	tr, _ := ts.AddTrigger("t1", "event_a", nil, TriggerAction{Kind: "workflow"})

	list := ts.ListTriggers()
	if len(list) != 1 {
		t.Fatalf("list = %d, want 1", len(list))
	}

	if !ts.EnableTrigger(tr.ID, false) {
		t.Error("EnableTrigger(false) should return true")
	}
	matched := ts.Evaluate("event_a", nil)
	if len(matched) != 0 {
		t.Error("disabled trigger should not match")
	}

	ts.EnableTrigger(tr.ID, true)
	matched = ts.Evaluate("event_a", nil)
	if len(matched) != 1 {
		t.Error("re-enabled trigger should match")
	}

	if !ts.RemoveTrigger(tr.ID) {
		t.Error("RemoveTrigger should return true")
	}
	if ts.RemoveTrigger(tr.ID) {
		t.Error("second RemoveTrigger should return false")
	}
}

func TestTriggerService_ValidationErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, err := ts.AddTrigger("bad", "", nil, TriggerAction{Kind: "workflow"})
	if err == nil {
		t.Error("expected error for empty event")
	}

	_, err = ts.AddTrigger("bad", "event", nil, TriggerAction{})
	if err == nil {
		t.Error("expected error for empty action kind")
	}
}

func TestTriggerService_PersistReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "triggers.json")

	ts1 := NewTriggerService(path)
	_, _ = ts1.AddTrigger("persist", "event_x", map[string]string{"k": "v"},
		TriggerAction{Kind: "workflow", TemplateID: "tpl-2"})

	ts2 := NewTriggerService(path)
	list := ts2.ListTriggers()
	if len(list) != 1 {
		t.Fatalf("reloaded %d triggers, want 1", len(list))
	}
	if list[0].Name != "persist" {
		t.Errorf("name = %q", list[0].Name)
	}
	if list[0].Action.TemplateID != "tpl-2" {
		t.Errorf("template_id = %q", list[0].Action.TemplateID)
	}
}

func TestTriggerService_RegisterHooks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTriggerService(filepath.Join(dir, "triggers.json"))

	_, _ = ts.AddTrigger("a", "event_1", nil, TriggerAction{Kind: "message"})
	_, _ = ts.AddTrigger("b", "event_1", nil, TriggerAction{Kind: "message"})
	_, _ = ts.AddTrigger("c", "event_2", nil, TriggerAction{Kind: "workflow"})

	registered := make(map[string]int)
	ts.RegisterHooks(func(event string, _ func(map[string]any) error) {
		registered[event]++
	})

	if registered["event_1"] != 1 {
		t.Errorf("event_1 registered %d times, want 1", registered["event_1"])
	}
	if registered["event_2"] != 1 {
		t.Errorf("event_2 registered %d times, want 1", registered["event_2"])
	}
}
