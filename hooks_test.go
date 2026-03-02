package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type hookRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *hookRecorder) Fire(event string, data map[string]any) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return 1
}

func (r *hookRecorder) getEvents() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

func TestWorkflowHooks_StartComplete(t *testing.T) {
	t.Parallel()

	runner := &mockToolRunner{results: map[string]string{"echo": "ok"}}
	engine, store := newTestEngine(t, runner)
	m := engine.metrics

	recorder := &hookRecorder{}
	engine.SetHooks(recorder)

	wf := NewWorkflow("wf1", "Hooks", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	events := recorder.getEvents()
	expected := []string{"workflow_started", "workflow_step_started", "workflow_step_completed", "workflow_completed"}
	if len(events) != len(expected) {
		t.Fatalf("events = %v, want %v", events, expected)
	}
	for i, e := range expected {
		if events[i] != e {
			t.Errorf("event[%d] = %q, want %q", i, events[i], e)
		}
	}

	if m.HooksFired.Load() != int64(len(expected)) {
		t.Errorf("HooksFired = %d, want %d", m.HooksFired.Load(), len(expected))
	}
}

func TestWorkflowHooks_StepFailed(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{err: errors.New("boom")}
	engine, store := newTestEngine(t, runner)

	recorder := &hookRecorder{}
	engine.SetHooks(recorder)

	wf := NewWorkflow("wf1", "FailHooks", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	events := recorder.getEvents()
	hasStepFailed := false
	hasWorkflowFailed := false
	for _, e := range events {
		if e == "workflow_step_failed" {
			hasStepFailed = true
		}
		if e == "workflow_failed" {
			hasWorkflowFailed = true
		}
	}
	if !hasStepFailed {
		t.Error("missing workflow_step_failed event")
	}
	if !hasWorkflowFailed {
		t.Error("missing workflow_failed event")
	}
}

func TestWorkflowHooks_Cancelled(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	recorder := &hookRecorder{}
	engine.SetHooks(recorder)

	wf := NewWorkflow("wf1", "CancelHooks", "telegram:1", nil)
	wf.State = StateRunning
	_ = store.Save(wf)
	_ = engine.Cancel("wf1")

	events := recorder.getEvents()
	if len(events) != 1 || events[0] != "workflow_cancelled" {
		t.Errorf("events = %v, want [workflow_cancelled]", events)
	}
}

func TestWorkflowHooks_NilSafe(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{"echo": "ok"}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "NilHooks", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}
