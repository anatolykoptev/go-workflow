package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStartAsync_NonBlocking(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{results: map[string]string{"echo": "hello"}}
	engine, store := newTestEngine(t, runner)

	var notified sync.WaitGroup
	notified.Add(1)
	var notifiedWf *Workflow
	engine.SetCompletionNotifier(func(wf *Workflow) {
		notifiedWf = wf
		notified.Done()
	})

	wf := NewWorkflow("wf1", "Async", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.StartAsync(context.Background(), "wf1")
	if err != nil {
		t.Fatal(err)
	}

	notified.Wait()

	if notifiedWf == nil {
		t.Fatal("completion notifier was not called")
	}
	if notifiedWf.State != StateCompleted {
		t.Errorf("notified state = %s, want completed", notifiedWf.State)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("workflow state = %s, want completed", loaded.State)
	}
}

func TestStartAsync_FailureNotifies(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{err: errors.New("boom")}
	engine, store := newTestEngine(t, runner)

	var notified sync.WaitGroup
	notified.Add(1)
	var notifiedState WorkflowState
	engine.SetCompletionNotifier(func(wf *Workflow) {
		notifiedState = wf.State
		notified.Done()
	})

	wf := NewWorkflow("wf1", "FailAsync", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.StartAsync(context.Background(), "wf1")

	notified.Wait()
	if notifiedState != StateFailed {
		t.Errorf("notified state = %s, want failed", notifiedState)
	}
}

func TestCancel_Notifies(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	var notifiedState WorkflowState
	engine.SetCompletionNotifier(func(wf *Workflow) {
		notifiedState = wf.State
	})

	wf := NewWorkflow("wf1", "CancelMe", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	wf.State = StateRunning
	_ = store.Save(wf)

	_ = engine.Cancel("wf1")

	if notifiedState != StateCancelled {
		t.Errorf("notified state = %s, want cancelled", notifiedState)
	}
}

func TestPauseAll(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf1 := NewWorkflow("wf1", "Running1", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	wf1.State = StateRunning
	_ = store.Save(wf1)

	wf2 := NewWorkflow("wf2", "Running2", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	wf2.State = StateRunning
	_ = store.Save(wf2)

	wf3 := NewWorkflow("wf3", "Pending", "telegram:1", nil)
	_ = store.Save(wf3)

	paused := engine.PauseAll()
	if paused != 2 {
		t.Errorf("paused = %d, want 2", paused)
	}

	for _, id := range []string{"wf1", "wf2"} {
		loaded, _ := store.Load(id)
		if loaded.State != StatePaused {
			t.Errorf("%s state = %s, want paused", id, loaded.State)
		}
	}

	loaded3, _ := store.Load("wf3")
	if loaded3.State != StatePending {
		t.Errorf("wf3 state = %s, want pending (unchanged)", loaded3.State)
	}
}

func TestRecoverAll(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"echo": "ok"}}
	engine, store := newTestEngine(t, runner)

	// Simulate crash: workflow stuck in running, step stuck in running
	wf := NewWorkflow("wf1", "Crashed", "owner", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepCompleted},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepRunning,
			DependsOn: []string{"s1"}},
	})
	wf.State = StateRunning
	_ = store.Save(wf)

	// Completed workflow should be untouched
	wf2 := NewWorkflow("wf2", "Done", "owner", nil)
	wf2.State = StateCompleted
	_ = store.Save(wf2)

	recovered := engine.RecoverAll(context.Background())
	if len(recovered) != 1 || recovered[0] != "wf1" {
		t.Errorf("recovered = %v, want [wf1]", recovered)
	}

	// Wait for async execution
	time.Sleep(200 * time.Millisecond)

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("wf1 state = %s, want completed", loaded.State)
	}

	// Verify completed workflow untouched
	loaded2, _ := store.Load("wf2")
	if loaded2.State != StateCompleted {
		t.Errorf("wf2 state = %s, want completed (should be unchanged)", loaded2.State)
	}
}
