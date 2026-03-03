package workflow

import (
	"context"
	"testing"
)

func TestInterruptBefore_PausesBeforeStep(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "InterruptBefore", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t2"}, DependsOn: []string{"s1"}, State: StepPending},
	})
	wf.InterruptBefore = []string{"s2"}
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval", loaded.State)
	}
	if loaded.GetStep("s1").State != StepCompleted {
		t.Errorf("s1 state = %s, want completed", loaded.GetStep("s1").State)
	}
	if loaded.GetStep("s2").State != StepPending {
		t.Errorf("s2 state = %s, want pending", loaded.GetStep("s2").State)
	}
}

func TestInterruptAfter_PausesAfterStep(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "InterruptAfter", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t2"}, DependsOn: []string{"s1"}, State: StepPending},
	})
	wf.InterruptAfter = []string{"s1"}
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval", loaded.State)
	}
	if loaded.GetStep("s1").State != StepCompleted {
		t.Errorf("s1 state = %s, want completed", loaded.GetStep("s1").State)
	}
	if loaded.GetStep("s2").State != StepPending {
		t.Errorf("s2 state = %s, want pending", loaded.GetStep("s2").State)
	}
}

func TestInterruptBefore_ResumeAfterApproval(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "InterruptResume", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t2"}, DependsOn: []string{"s1"}, State: StepPending},
	})
	wf.InterruptBefore = []string{"s2"}
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("state = %s, want waiting_approval", loaded.State)
	}

	if err := engine.HandleApproval("wf1", true); err != nil {
		t.Fatal(err)
	}
	_ = engine.RunToCompletion(context.Background(), "wf1")

	loaded, _ = store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	if loaded.GetStep("s1").State != StepCompleted {
		t.Errorf("s1 state = %s, want completed", loaded.GetStep("s1").State)
	}
	if loaded.GetStep("s2").State != StepCompleted {
		t.Errorf("s2 state = %s, want completed", loaded.GetStep("s2").State)
	}
}

func TestInterruptBefore_NoInterrupt_RunsNormally(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "NoInterrupt", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
	})
	// No InterruptBefore or InterruptAfter set
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	if loaded.GetStep("s1").State != StepCompleted {
		t.Errorf("s1 state = %s, want completed", loaded.GetStep("s1").State)
	}
}
