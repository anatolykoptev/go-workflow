package workflow

import (
	"context"
	"testing"
)

func TestLocalDispatcher_Dispatch(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf-dispatch-1", "DispatchSingle", "test:1", []Step{
		{ID: "s1", Kind: StepNoop, Config: map[string]any{}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	// Start the workflow so it transitions to running state.
	if _, err := engine.startWorkflow("wf-dispatch-1"); err != nil {
		t.Fatal(err)
	}

	d := NewLocalDispatcher(engine)
	if err := d.Dispatch(context.Background(), "wf-dispatch-1", "s1", StepNoop); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	loaded, ok := store.Load("wf-dispatch-1")
	if !ok {
		t.Fatal("workflow not found after dispatch")
	}

	step := loaded.GetStep("s1")
	if step == nil {
		t.Fatal("step s1 not found")
	}
	if step.State != StepCompleted {
		t.Errorf("step state = %s, want completed", step.State)
	}
}

func TestLocalDispatcher_DispatchBatch(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf-dispatch-2", "DispatchBatch", "test:1", []Step{
		{ID: "s1", Kind: StepNoop, Config: map[string]any{}, State: StepPending},
		{ID: "s2", Kind: StepNoop, Config: map[string]any{}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.startWorkflow("wf-dispatch-2"); err != nil {
		t.Fatal(err)
	}

	d := NewLocalDispatcher(engine)
	err := d.DispatchBatch(
		context.Background(),
		"wf-dispatch-2",
		[]string{"s1", "s2"},
		[]StepKind{StepNoop, StepNoop},
	)
	if err != nil {
		t.Fatalf("DispatchBatch returned error: %v", err)
	}

	loaded, ok := store.Load("wf-dispatch-2")
	if !ok {
		t.Fatal("workflow not found after dispatch")
	}

	for _, sid := range []string{"s1", "s2"} {
		step := loaded.GetStep(sid)
		if step == nil {
			t.Fatalf("step %s not found", sid)
		}
		if step.State != StepCompleted {
			t.Errorf("step %s state = %s, want completed", sid, step.State)
		}
	}
}

func TestAdvance_UsesDispatcher(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf-advance-1", "AdvanceDispatch", "test:1", []Step{
		{ID: "s1", Kind: StepNoop, Config: map[string]any{}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	if err := engine.Start(context.Background(), "wf-advance-1"); err != nil {
		t.Fatal(err)
	}

	loaded, ok := store.Load("wf-advance-1")
	if !ok {
		t.Fatal("workflow not found")
	}
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

func TestWithDispatcher_Option(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	spy := &spyDispatcher{}
	engine := NewEngine(store, WithDispatcher(spy))

	wf := NewWorkflow("wf-spy-1", "SpyDispatch", "test:1", []Step{
		{ID: "s1", Kind: StepNoop, Config: map[string]any{}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.startWorkflow("wf-spy-1"); err != nil {
		t.Fatal(err)
	}

	_, _ = engine.Advance(context.Background(), "wf-spy-1")

	if spy.dispatchCount != 1 {
		t.Errorf("dispatch called %d times, want 1", spy.dispatchCount)
	}
	if spy.lastWorkflowID != "wf-spy-1" {
		t.Errorf("last workflow ID = %q, want wf-spy-1", spy.lastWorkflowID)
	}
	if spy.lastStepID != "s1" {
		t.Errorf("last step ID = %q, want s1", spy.lastStepID)
	}
}

// spyDispatcher records calls for verification without executing steps.
type spyDispatcher struct {
	dispatchCount  int
	batchCount     int
	lastWorkflowID string
	lastStepID     string
}

func (s *spyDispatcher) Dispatch(_ context.Context, workflowID, stepID string, _ StepKind) error {
	s.dispatchCount++
	s.lastWorkflowID = workflowID
	s.lastStepID = stepID
	return nil
}

func (s *spyDispatcher) DispatchBatch(_ context.Context, workflowID string, stepIDs []string, _ []StepKind) error {
	s.batchCount++
	s.lastWorkflowID = workflowID
	if len(stepIDs) > 0 {
		s.lastStepID = stepIDs[0]
	}
	return nil
}
