package workflow

import (
	"encoding/json"
	"testing"
)

// TestApprovalStatusSSOT_Issue23 reproduces the root cause of issue #23:
// wf_status's pending_approval and HandleApproval's resolved target must
// agree on which approval gate a waiting workflow is blocked on. Before the
// fix, wf_status scanned Steps with no break (landing on the LAST pending
// approval) while HandleApproval broke on the first match — so with 2+
// pending approval gates ahead they disagreed. Both now route through
// Workflow.BlockingStep(), which defers to the authoritative CurrentStep.
func TestApprovalStatusSSOT_Issue23(t *testing.T) {
	t.Parallel()

	// Two approval gates ahead, both StepPending (the common shape from #19:
	// not-yet-reached approval steps default to StepPending from creation).
	// CurrentStep points at the FIRST one — the gate the workflow is actually
	// halted on.
	steps := []Step{
		{ID: "compose-content", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "go-live", Kind: StepApproval, Config: map[string]any{}, DependsOn: []string{"compose-content"}, State: StepPending},
	}

	// --- wf_status side: pending_approval must equal the authoritative gate. ---
	mcpURL, eng := setupMCPToolsTest(t)
	wfStatus := NewWorkflow("wf-ssot-status", "SSOT", "user", steps)
	wfStatus.CurrentStep = "compose-content"
	wfStatus.State = StateWaitingApproval
	if err := eng.Store().Save(wfStatus); err != nil {
		t.Fatalf("Save status wf: %v", err)
	}

	statusResult := callWFTool(t, mcpURL, "wf_status", map[string]any{
		"workflow_id": "wf-ssot-status",
	})
	var status wfStatusOutput
	if err := json.Unmarshal([]byte(statusResult), &status); err != nil {
		t.Fatalf("unmarshal wf_status: %v\nraw: %s", err, statusResult)
	}
	if status.PendingApproval != "compose-content" {
		t.Errorf("wf_status.pending_approval = %q, want %q (authoritative CurrentStep; "+
			"the no-break scan used to land on the LAST pending approval)", status.PendingApproval, "compose-content")
	}

	// --- HandleApproval side: must resolve the SAME authoritative gate. ---
	wfApprove := NewWorkflow("wf-ssot-approve", "SSOT", "user", cloneSteps(steps))
	wfApprove.CurrentStep = "compose-content"
	wfApprove.State = StateWaitingApproval
	if err := eng.Store().Save(wfApprove); err != nil {
		t.Fatalf("Save approve wf: %v", err)
	}

	if err := eng.HandleApproval("wf-ssot-approve", true); err != nil {
		t.Fatalf("HandleApproval: %v", err)
	}
	loaded, _ := eng.Store().Load("wf-ssot-approve")
	if got := loaded.GetStep("compose-content"); got == nil || got.State != StepCompleted {
		stepState := "<missing>"
		if got != nil {
			stepState = string(got.State)
		}
		t.Errorf("HandleApproval resolved compose-content state = %s, want completed "+
			"(wf_status and HandleApproval must target the same gate)", stepState)
	}
	if got := loaded.GetStep("go-live"); got != nil && got.State == StepCompleted {
		t.Error("HandleApproval completed go-live, the WRONG gate — should have stayed pending")
	}

	// The two answers must agree.
	if status.PendingApproval != "compose-content" {
		t.Fatalf("invariant: wf_status and HandleApproval disagree on the blocking gate")
	}
}

// TestBlockingStep_Authoritative covers BlockingStep deferring to CurrentStep
// when State==StateWaitingApproval.
func TestBlockingStep_Authoritative(t *testing.T) {
	t.Parallel()
	wf := NewWorkflow("wf", "T", "u", []Step{
		{ID: "first", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "second", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "first"
	wf.State = StateWaitingApproval

	got := wf.BlockingStep()
	if got == nil {
		t.Fatal("BlockingStep returned nil for waiting_approval workflow")
	}
	if got.ID != "first" {
		t.Errorf("BlockingStep = %q, want %q (CurrentStep is authoritative)", got.ID, "first")
	}
}

// TestBlockingStep_NotWaiting covers the nil-return path: when State is not
// StateWaitingApproval, BlockingStep must return nil regardless of step states.
func TestBlockingStep_NotWaiting(t *testing.T) {
	t.Parallel()
	wf := NewWorkflow("wf", "T", "u", []Step{
		{ID: "first", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "first"
	wf.State = StateRunning

	if got := wf.BlockingStep(); got != nil {
		t.Errorf("BlockingStep = %v, want nil when State != waiting_approval", got)
	}
}

// TestBlockingStep_CurrentStepPointsAtNonApproval_FallsBackToScan covers
// the corrected contract: when CurrentStep names a step that exists but is
// NOT a pending approval gate, BlockingStep must NOT trust it blindly — it
// falls through to the scan and returns the real pending approval step.
// (Previously this test asserted the buggy behavior of returning the
// non-approval CurrentStep as-is, which regressed the parallel-dispatch
// case — see TestBlockingStep_CurrentStepRacedToNonApprovalSibling_FallsBackToScan.)
func TestBlockingStep_CurrentStepPointsAtNonApproval_FallsBackToScan(t *testing.T) {
	t.Parallel()
	wf := NewWorkflow("wf", "T", "u", []Step{
		{ID: "tool", Kind: StepTool, Config: map[string]any{}, State: StepRunning},
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "tool" // exists but not a pending approval — must fall back to scan
	wf.State = StateWaitingApproval

	got := wf.BlockingStep()
	if got == nil {
		t.Fatal("BlockingStep returned nil; expected fallback scan to find the pending approval gate")
	}
	if got.ID != "gate" {
		t.Errorf("BlockingStep = %q, want %q (CurrentStep names a non-approval step; fallback scan must find the real gate)", got.ID, "gate")
	}
}

// TestBlockingStep_CurrentStepRacedToNonApprovalSibling_FallsBackToScan is a
// DETERMINISTIC regression test for the parallel-dispatch race fixed in the
// #23 follow-up. Under runParallel/DispatchBatch, sibling steps in the same
// batch each write CurrentStep inside store.Modify; last-write-wins can leave
// CurrentStep pointing at a non-approval sibling after the real approval
// gate has already paused the workflow into StateWaitingApproval. Rather than
// rely on goroutine scheduling (flaky), this test constructs the
// post-race Workflow state directly: StateWaitingApproval, a pending approval
// gate present in Steps, and CurrentStep set to a DIFFERENT non-approval
// sibling's ID — and asserts BlockingStep still returns the real approval
// gate via the fallback scan, not nil and not the wrong sibling.
func TestBlockingStep_CurrentStepRacedToNonApprovalSibling_FallsBackToScan(t *testing.T) {
	t.Parallel()
	// Simulated post-race snapshot: gate A paused the workflow, then sibling B
	// (an independent non-approval branch in the same parallel batch) finished
	// last and overwrote CurrentStep = "B".
	wf := NewWorkflow("wf-race", "T", "u", []Step{
		{ID: "A", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "B", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepCompleted},
	})
	wf.State = StateWaitingApproval
	wf.CurrentStep = "B" // last-writer-wins: non-approval sibling overwrote the gate's ID

	got := wf.BlockingStep()
	if got == nil {
		t.Fatal("BlockingStep returned nil; expected fallback scan to find gate A despite the racy CurrentStep")
	}
	if got.ID != "A" {
		t.Errorf("BlockingStep = %q, want %q (CurrentStep raced to non-approval sibling B; fallback scan must return the real pending gate A)", got.ID, "A")
	}
	if got.Kind != StepApproval || got.State != StepPending {
		t.Errorf("BlockingStep returned step %q kind=%s state=%s; want a pending approval gate", got.ID, got.Kind, got.State)
	}
}

// TestBlockingStep_LegacyFallback covers the fallback path: when CurrentStep
// is empty (a legacy workflow persisted before CurrentStep was reliable),
// BlockingStep scans for the first pending approval step.
func TestBlockingStep_LegacyFallback(t *testing.T) {
	t.Parallel()
	wf := NewWorkflow("wf", "T", "u", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "" // legacy: no CurrentStep → fallback scan
	wf.State = StateWaitingApproval

	got := wf.BlockingStep()
	if got == nil {
		t.Fatal("BlockingStep returned nil; expected legacy fallback to pending approval")
	}
	if got.ID != "gate" {
		t.Errorf("BlockingStep fallback = %q, want %q", got.ID, "gate")
	}
}

// TestBlockingStep_NothingToResolve covers the nil path where State is
// waiting_approval but CurrentStep is empty AND no pending approval step
// exists at all — BlockingStep returns nil.
func TestBlockingStep_NothingToResolve(t *testing.T) {
	t.Parallel()
	wf := NewWorkflow("wf", "T", "u", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepCompleted},
	})
	wf.CurrentStep = "" // no CurrentStep, and the only approval is completed
	wf.State = StateWaitingApproval

	if got := wf.BlockingStep(); got != nil {
		t.Errorf("BlockingStep = %v, want nil when no pending approval exists", got)
	}
}

// TestBlockingStep_InterruptPauseDoesNotBypassDownstreamApprovalGate is the
// round-4 regression test for issue #23. It constructs the exact repro:
// s1: StepTool with InterruptBefore=["s1"], s2: StepApproval downstream of s1.
// The workflow is paused at the s1 interrupt checkpoint (CurrentStep="s1",
// State=StateWaitingApproval, s1 still StepPending, s2 also StepPending but
// NOT yet reached). Resuming the interrupt pause via HandleApproval must NOT
// auto-complete s2 — s2 must remain untouched (still StepPending) so it gets
// presented for real approval when the workflow actually reaches it.
//
// Under round 3's code (commit f3dce51), BlockingStep saw GetStep("s1") was a
// non-approval step and fell back to the scan, which found s2 (the unrelated
// downstream approval gate) and returned it — HandleApproval then marked s2
// StepCompleted with the approval sentinel, silently bypassing a human
// approval step that was never actually presented to anyone. This test FAILS
// against f3dce51 (BlockingStep returns s2) and PASSES after the round-4 fix
// (BlockingStep recognizes the active interrupt pause and returns nil).
func TestBlockingStep_InterruptPauseDoesNotBypassDownstreamApprovalGate(t *testing.T) {
	t.Parallel()
	// s1: StepTool paused via interrupt_before; s2: StepApproval downstream,
	// not yet reached (still StepPending from creation).
	wf := NewWorkflow("wf-interrupt-gate", "T", "u", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t1"}, State: StepPending},
		{ID: "s2", Kind: StepApproval, Config: map[string]any{}, DependsOn: []string{"s1"}, State: StepPending},
	})
	wf.State = StateWaitingApproval
	wf.CurrentStep = "s1" // the step the interrupt_before paused on
	wf.InterruptBefore = []string{"s1"}

	got := wf.BlockingStep()
	if got != nil {
		t.Errorf("BlockingStep = %q (kind=%s, state=%s), want nil — CurrentStep names the "+
			"interrupt pause point s1, not a pending approval gate; the scan must NOT grab the "+
			"unrelated downstream approval gate s2 (it was never presented for approval)",
			got.ID, got.Kind, got.State)
	}
	// Belt-and-suspenders: the downstream gate must still be pending (untouched).
	if s2 := wf.GetStep("s2"); s2 != nil && s2.State != StepPending {
		t.Errorf("s2 state = %s, want StepPending — BlockingStep must not surface s2 as the gate", s2.State)
	}
}

// cloneSteps returns a shallow copy of the step slice so two test workflows
// don't share underlying Step values (DependsOn slices are shared, which is
// fine for read-only use here).
func cloneSteps(in []Step) []Step {
	out := make([]Step, len(in))
	copy(out, in)
	return out
}
