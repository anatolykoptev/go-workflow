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

func TestInterruptBefore_NonApprovalStep_ExecutesForReal(t *testing.T) {
	t.Parallel()
	callCount := 0
	runner := &countingToolRunner{callCount: &callCount}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wfzzz", "InterruptResumeVerify", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t2"}, DependsOn: []string{"s1"}, State: StepPending},
	})
	wf.InterruptBefore = []string{"s2"}
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wfzzz")
	loaded, _ := store.Load("wfzzz")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("state = %s, want waiting_approval", loaded.State)
	}
	if err := engine.HandleApproval("wfzzz", true); err != nil {
		t.Fatal(err)
	}
	_ = engine.RunToCompletion(context.Background(), "wfzzz")

	loaded, _ = store.Load("wfzzz")
	s2 := loaded.GetStep("s2")
	if callCount != 2 {
		t.Errorf("REGRESSION: tool executor called %d times, want 2 (s1+s2) — s2's real executor was bypassed", callCount)
	}
	if s2.Result == approvalResult {
		t.Errorf("REGRESSION: s2.Result = %q — HandleApproval overwrote the tool step's result with the approval sentinel instead of letting it execute", s2.Result)
	}
}

// TestInterruptBefore_ResumeDoesNotBypassDownstreamApprovalGate is the
// end-to-end round-4 regression for issue #23. It mirrors
// TestInterruptBefore_ResumeAfterApproval's shape but adds a downstream
// StepApproval gate (s2: approval) after the interrupted tool step (s1).
// Resuming the s1 interrupt checkpoint via HandleApproval(true) must run s1
// for real and then STOP at s2 — s2 must still be StepPending (workflow back
// in StateWaitingApproval waiting on the real gate), NOT silently completed
// by the approval sentinel. Under round 3's code, BlockingStep's fallback
// scan grabbed s2 during the interrupt resume and HandleApproval marked it
// StepCompleted, bypassing the human approval gate entirely.
func TestInterruptBefore_ResumeDoesNotBypassDownstreamApprovalGate(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-r4", "InterruptThenApproval", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
		{ID: "s2", Kind: StepApproval, Config: map[string]any{}, DependsOn: []string{"s1"}, State: StepPending},
	})
	wf.InterruptBefore = []string{"s1"}
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf-r4")

	loaded, _ := store.Load("wf-r4")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("after Start: state = %s, want waiting_approval (interrupt_before on s1)", loaded.State)
	}
	if got := loaded.GetStep("s1"); got.State != StepPending {
		t.Fatalf("after Start: s1 state = %s, want pending (interrupt paused before execution)", got.State)
	}

	// Resume the interrupt checkpoint — this must ONLY clear the interrupt
	// and flip state to running, NOT touch the downstream approval gate s2.
	if err := engine.HandleApproval("wf-r4", true); err != nil {
		t.Fatal(err)
	}
	_ = engine.RunToCompletion(context.Background(), "wf-r4")

	loaded, _ = store.Load("wf-r4")
	// s1 must have executed for real (completed by its tool executor).
	if got := loaded.GetStep("s1"); got.State != StepCompleted {
		t.Errorf("s1 state = %s, want completed (interrupt resume must run s1 for real)", got.State)
	}
	// s2 must STILL be pending — the real approval gate was never presented,
	// so it must not have been silently completed by the approval sentinel.
	s2 := loaded.GetStep("s2")
	if s2.State != StepPending {
		t.Errorf("REGRESSION: s2 state = %s, want pending — resuming the s1 interrupt silently "+
			"bypassed the downstream approval gate (it was never presented for real approval)", s2.State)
	}
	if s2.Result == approvalResult {
		t.Errorf("REGRESSION: s2.Result = %q — HandleApproval overwrote the approval gate's "+
			"result with the approval sentinel during the interrupt resume", s2.Result)
	}
	// The workflow must be back in waiting_approval, halted on the real s2 gate.
	if loaded.State != StateWaitingApproval {
		t.Errorf("workflow state = %s, want waiting_approval (halted on the real s2 approval gate)", loaded.State)
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
