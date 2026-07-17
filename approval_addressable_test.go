package workflow

import (
	"testing"
)

// Tests for issue #24: addressable approval targeting via an explicit step_id.
// wfApproveInput gains an optional StepID; when set, HandleApproval/
// HandleApprovalWithData resolve THAT specific gate via GetStep instead of
// the BlockingStep() single-gate auto-resolution. When empty, behavior is
// unchanged (BlockingStep fallback) — see the existing *Issue23 tests.

// TestHandleApproval_StepID_TargetsExplicitGate proves the explicit step_id
// path resolves the NAMED gate among multiple CONCURRENTLY-REACHABLE gates,
// not whichever gate BlockingStep() would auto-resolve. Two independent
// pending approval gates are present (neither depends on the other — both are
// immediately reachable); CurrentStep points at "gate-a" (so BlockingStep()
// would resolve gate-a), but the call targets "gate-b" explicitly — gate-b
// must be completed and gate-a must stay pending. Falsification: if stepID is
// ignored (reverted to always-BlockingStep), gate-a gets completed instead and
// this test fails.
func TestHandleApproval_StepID_TargetsExplicitGate(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-target", "Target", "u", []Step{
		{ID: "gate-a", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "gate-b", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate-a" // BlockingStep() would resolve this one
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := engine.HandleApproval("wf-target", true, "gate-b"); err != nil {
		t.Fatalf("HandleApproval with step_id: %v", err)
	}

	loaded, _ := store.Load("wf-target")
	if got := loaded.GetStep("gate-b"); got == nil || got.State != StepCompleted {
		state := "<missing>"
		if got != nil {
			state = string(got.State)
		}
		t.Errorf("gate-b state = %s, want completed (explicitly targeted by step_id)", state)
	}
	if got := loaded.GetStep("gate-a"); got != nil && got.State == StepCompleted {
		t.Error("gate-a was completed — step_id should have targeted gate-b, not the BlockingStep gate")
	}
	if loaded.State != StateRunning {
		t.Errorf("workflow state = %s, want running", loaded.State)
	}
}

// TestHandleApproval_StepID_NonExistentStep asserts a clear error and NO
// mutation when step_id names a step that doesn't exist.
func TestHandleApproval_StepID_NonExistentStep(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-missing", "Missing", "u", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	err := engine.HandleApproval("wf-missing", true, "no-such-step")
	if err == nil {
		t.Fatal("HandleApproval with non-existent step_id should error, got nil")
	}

	loaded, _ := store.Load("wf-missing")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (no mutation on error)", loaded.State)
	}
	if got := loaded.GetStep("gate"); got.State != StepPending {
		t.Errorf("gate state = %s, want pending (no mutation on error)", got.State)
	}
}

// TestHandleApproval_StepID_NotPendingApproval asserts a clear error and NO
// mutation when step_id names a step that exists but is not a pending
// approval gate — here a plain tool step.
func TestHandleApproval_StepID_NotPendingApproval_ToolStep(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-tool", "ToolTarget", "u", []Step{
		{ID: "tool", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	err := engine.HandleApproval("wf-tool", true, "tool")
	if err == nil {
		t.Fatal("HandleApproval targeting a non-approval tool step should error, got nil")
	}

	loaded, _ := store.Load("wf-tool")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (no mutation on error)", loaded.State)
	}
	if got := loaded.GetStep("tool"); got.State != StepPending {
		t.Errorf("tool state = %s, want pending (no mutation on error)", got.State)
	}
	if got := loaded.GetStep("gate"); got.State != StepPending {
		t.Errorf("gate state = %s, want pending (no mutation on error)", got.State)
	}
}

// TestHandleApproval_StepID_NotPendingApproval_AlreadyCompleted asserts a
// clear error when step_id names an approval step that already completed.
func TestHandleApproval_StepID_NotPendingApproval_AlreadyCompleted(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-done", "DoneTarget", "u", []Step{
		{ID: "done", Kind: StepApproval, Config: map[string]any{}, State: StepCompleted},
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	err := engine.HandleApproval("wf-done", true, "done")
	if err == nil {
		t.Fatal("HandleApproval targeting an already-completed approval step should error, got nil")
	}

	loaded, _ := store.Load("wf-done")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (no mutation on error)", loaded.State)
	}
}

// TestHandleApproval_StepID_NotReachable_DependencyPending asserts a clear
// error and NO mutation when step_id names a pending approval gate whose
// DependsOn includes a step that is NOT yet terminal-satisfied (here a tool
// step still StepPending). This is the reachability guard from issue #24: a
// downstream approval gate that hasn't been reached yet must never be
// completable out of order, or findAllRunnable would never schedule it again
// and the human approval that was supposed to happen there would be silently
// bypassed. Falsification: revert the stepDepsSatisfied check and this test
// fails (the gate gets completed despite its upstream being pending).
func TestHandleApproval_StepID_NotReachable_DependencyPending(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-unreachable", "Unreachable", "u", []Step{
		{ID: "upstream", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
		{ID: "downstream", Kind: StepApproval, Config: map[string]any{}, DependsOn: []string{"upstream"}, State: StepPending},
	})
	wf.CurrentStep = "upstream"
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	err := engine.HandleApproval("wf-unreachable", true, "downstream")
	if err == nil {
		t.Fatal("HandleApproval targeting an unreachable gate should error, got nil")
	}

	loaded, _ := store.Load("wf-unreachable")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (no mutation on error)", loaded.State)
	}
	if got := loaded.GetStep("downstream"); got.State != StepPending {
		t.Errorf("downstream state = %s, want pending (no mutation on error)", got.State)
	}
	if got := loaded.GetStep("upstream"); got.State != StepPending {
		t.Errorf("upstream state = %s, want pending (no mutation on error)", got.State)
	}
}

// TestHandleApproval_StepID_Empty_FallsBackToBlockingStep confirms the empty
// step_id path is unchanged — it resolves via BlockingStep() exactly as
// before issue #24. (The existing *Issue23 tests also cover this once updated
// to the new signature; this one is a focused re-statement.)
func TestHandleApproval_StepID_Empty_FallsBackToBlockingStep(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-empty", "Empty", "u", []Step{
		{ID: "first", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "second", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "first" // BlockingStep resolves this
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := engine.HandleApproval("wf-empty", true, ""); err != nil {
		t.Fatalf("HandleApproval empty step_id: %v", err)
	}

	loaded, _ := store.Load("wf-empty")
	if got := loaded.GetStep("first"); got == nil || got.State != StepCompleted {
		state := "<missing>"
		if got != nil {
			state = string(got.State)
		}
		t.Errorf("first state = %s, want completed (BlockingStep fallback with empty step_id)", state)
	}
	if got := loaded.GetStep("second"); got != nil && got.State == StepCompleted {
		t.Error("second was completed — empty step_id should resolve only the BlockingStep gate")
	}
}

// TestHandleApprovalWithData_StepID_TargetsExplicitGate mirrors the
// HandleApproval targeting test but exercises the WithData path: the
// structured data must land on the explicitly-targeted gate's context. The
// two gates are independent (no DependsOn between them) so both are
// concurrently reachable — see the reachability guard in HandleApproval.
func TestHandleApprovalWithData_StepID_TargetsExplicitGate(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-data-target", "DataTarget", "u", []Step{
		{ID: "first", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "second", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "first" // BlockingStep would resolve this
	wf.State = StateWaitingApproval
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data := map[string]any{"choice": "B"}
	if err := engine.HandleApprovalWithData("wf-data-target", true, data, "second"); err != nil {
		t.Fatalf("HandleApprovalWithData with step_id: %v", err)
	}

	loaded, _ := store.Load("wf-data-target")
	if got := loaded.GetStep("second"); got == nil || got.State != StepCompleted {
		state := "<missing>"
		if got != nil {
			state = string(got.State)
		}
		t.Errorf("second state = %s, want completed (explicitly targeted)", state)
	}
	ctx, ok := loaded.Context["second"].(map[string]any)
	if !ok {
		t.Fatalf("expected map in context[second], got %T", loaded.Context["second"])
	}
	if ctx["choice"] != "B" {
		t.Errorf("context[second].choice = %v, want B", ctx["choice"])
	}
	if got := loaded.GetStep("first"); got != nil && got.State == StepCompleted {
		t.Error("first was completed — step_id should have targeted second, not the BlockingStep gate")
	}
}
