package workflow

import (
	"context"
	"testing"
	"time"
)

// Tests for issue #25: optional per-gate approval timeout with auto-cancel via
// the watchdog. An approval step may set Config["approval_timeout_ms"] (a
// relative duration in ms); handleApprovalRequired computes an absolute
// deadline (now + timeout) and stores it in
// Context[stepID+"_approval_deadline_ms"]. The watchdog's new
// cancelExpiredApprovals() scans StateWaitingApproval workflows, scopes the
// check strictly to BlockingStep()'s current gate (never a stale deadline key
// from a previously-resolved gate), and calls Cancel once the deadline passes.
//
// Out of scope (and asserted NOT wired here): interrupt_before/interrupt_after
// checkpoint pauses — those set StateWaitingApproval too but BlockingStep()
// returns nil for them, so cancelExpiredApprovals skips them entirely.

// TestApprovalTimeout_DeadlineSetOnApprovalRequired drives the REAL engine
// path (Start -> ApprovalExecutor -> errApprovalRequired ->
// handleApprovalRequired) and asserts the absolute deadline lands in Context
// at approximately now + timeoutMS. Falsification: if handleApprovalRequired
// is reverted to not store the deadline, the context key is absent and this
// test fails.
func TestApprovalTimeout_DeadlineSetOnApprovalRequired(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	timeoutMS := int64(86400000) // 24h
	wf := NewWorkflow("wf-deadline", "ApprovalTimeout", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{
			"approval_timeout_ms": float64(timeoutMS),
		}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	before := time.Now().UnixMilli()
	if err := engine.Start(context.Background(), "wf-deadline"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	after := time.Now().UnixMilli()

	loaded, _ := store.Load("wf-deadline")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("state = %s, want waiting_approval", loaded.State)
	}

	deadline, ok := loaded.Context["gate_approval_deadline_ms"]
	if !ok {
		t.Fatal("expected gate_approval_deadline_ms in context, not set")
	}
	var dl int64
	switch d := deadline.(type) {
	case int64:
		dl = d
	case float64:
		dl = int64(d)
	default:
		t.Fatalf("deadline type = %T, want int64 or float64", deadline)
	}

	lo := before + timeoutMS
	hi := after + timeoutMS
	if dl < lo || dl > hi {
		t.Errorf("deadline = %d, want in [%d, %d] (now±jitter + %dms)", dl, lo, hi, timeoutMS)
	}
}

// TestApprovalTimeout_NoTimeoutConfigured_NoDeadlineSet asserts that an
// approval step WITHOUT approval_timeout_ms never gets a deadline key written
// — there is nothing to expire.
func TestApprovalTimeout_NoTimeoutConfigured_NoDeadlineSet(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf-notimeout", "NoTimeout", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := engine.Start(context.Background(), "wf-notimeout"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf-notimeout")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("state = %s, want waiting_approval", loaded.State)
	}
	if _, ok := loaded.Context["gate_approval_deadline_ms"]; ok {
		t.Error("gate_approval_deadline_ms should NOT be set when no timeout configured")
	}
}

// TestApprovalTimeout_ExpiredDeadline_Cancelled constructs a
// StateWaitingApproval workflow with a real approval gate and an already-past
// deadline in Context, calls cancelExpiredApprovals() directly, and asserts
// the workflow is now StateCancelled. Mirrors how suspend_hard_test.go tests
// resumeSuspended directly. Falsification: if cancelExpiredApprovals is
// removed (or never calls Cancel), the workflow stays WaitingApproval.
func TestApprovalTimeout_ExpiredDeadline_Cancelled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	past := time.Now().Add(-time.Hour).UnixMilli()
	wf := NewWorkflow("wf-expired", "Expired", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	wf.Context = map[string]any{"gate_approval_deadline_ms": int64(past)}
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	engine.cancelExpiredApprovals()

	loaded, _ := store.Load("wf-expired")
	if loaded.State != StateCancelled {
		t.Errorf("state = %s, want cancelled (deadline expired)", loaded.State)
	}
}

// TestApprovalTimeout_FutureDeadline_Untouched asserts a future deadline is
// left alone — still StateWaitingApproval.
func TestApprovalTimeout_FutureDeadline_Untouched(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	future := time.Now().Add(time.Hour).UnixMilli()
	wf := NewWorkflow("wf-future", "Future", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	wf.Context = map[string]any{"gate_approval_deadline_ms": float64(future)}
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	engine.cancelExpiredApprovals()

	loaded, _ := store.Load("wf-future")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (deadline in future)", loaded.State)
	}
}

// TestApprovalTimeout_NoDeadlineKey_Untouched asserts a waiting-approval
// workflow with no deadline key at all (no timeout was ever configured) is
// never cancelled, no matter how long it has been waiting.
func TestApprovalTimeout_NoDeadlineKey_Untouched(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf-nokey", "NoKey", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	// Long-ago UpdatedAt, but no deadline key — nothing to expire.
	wf.UpdatedAt = time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	engine.cancelExpiredApprovals()

	loaded, _ := store.Load("wf-nokey")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (no deadline configured)", loaded.State)
	}
}

// TestApprovalTimeout_StaleKeyFromResolvedGate_NotCancelled is the regression
// test for the BlockingStep()-scoping design rationale (point 3): a workflow
// that has ALREADY resolved one approval gate whose (now-expired) deadline key
// still sits in Context (resolving a gate does not necessarily clear its
// deadline key — same characteristic findSuspendDeadline's suspend keys
// already have), and is NOW waiting on a SECOND, different approval gate with
// a FUTURE deadline. A naive scan-all-Context-keys approach would see the
// stale expired key and wrongly cancel; scoping strictly to BlockingStep()'s
// current gate ID looks only at the live gate's (future) deadline and must NOT
// cancel. Falsification: if cancelExpiredApprovals scans all
// *_approval_deadline_ms keys instead of scoping to BlockingStep().ID, the
// stale expired key triggers a cancel and this test fails.
func TestApprovalTimeout_StaleKeyFromResolvedGate_NotCancelled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	past := time.Now().Add(-time.Hour).UnixMilli()
	future := time.Now().Add(time.Hour).UnixMilli()
	wf := NewWorkflow("wf-stale", "StaleKey", "test", []Step{
		{ID: "gate-old", Kind: StepApproval, Config: map[string]any{}, State: StepCompleted},
		{ID: "gate-new", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate-new"
	wf.State = StateWaitingApproval
	wf.Context = map[string]any{
		"gate-old_approval_deadline_ms": int64(past),   // stale, expired — must be ignored
		"gate-new_approval_deadline_ms": int64(future), // live, future — the only one that matters
	}
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	engine.cancelExpiredApprovals()

	loaded, _ := store.Load("wf-stale")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (stale expired key from resolved gate must not cancel; live gate deadline is future)", loaded.State)
	}
}

// TestApprovalTimeout_StaleKeyFromResolvedGate_NoLiveDeadline_NotCancelled
// extends the stale-key regression: even when the live (second) gate has NO
// deadline key at all (no timeout configured on it), a stale expired key from
// a previously-resolved gate must still NOT cancel the workflow.
func TestApprovalTimeout_StaleKeyFromResolvedGate_NoLiveDeadline_NotCancelled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	past := time.Now().Add(-time.Hour).UnixMilli()
	wf := NewWorkflow("wf-stale2", "StaleKeyNoLive", "test", []Step{
		{ID: "gate-old", Kind: StepApproval, Config: map[string]any{}, State: StepCompleted},
		{ID: "gate-new", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate-new"
	wf.State = StateWaitingApproval
	wf.Context = map[string]any{
		"gate-old_approval_deadline_ms": int64(past), // stale, expired — must be ignored
		// gate-new has no deadline key — no timeout on the live gate
	}
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	engine.cancelExpiredApprovals()

	loaded, _ := store.Load("wf-stale2")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (stale expired key must not cancel; live gate has no deadline)", loaded.State)
	}
}

// TestApprovalTimeout_InterruptPauseNotTouched asserts the out-of-scope
// interaction: a workflow paused via interrupt_before on a NON-approval step
// (State==StateWaitingApproval, BlockingStep() returns nil per #23 round 4)
// is never touched by cancelExpiredApprovals — there is no approval gate to
// time out. Matches the explicitly-out-of-scope note in issue #25's design.
func TestApprovalTimeout_InterruptPauseNotTouched(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf-interrupt", "InterruptPause", "test", []Step{
		{ID: "work", Kind: StepTool, Config: map[string]any{"tool": "x"}, State: StepPending},
	})
	wf.CurrentStep = "work"
	wf.InterruptBefore = []string{"work"}
	wf.State = StateWaitingApproval
	// Even plant a deadline key under the interrupt step's id — it is NOT an
	// approval gate, BlockingStep() returns nil, so it must be ignored.
	wf.Context = map[string]any{"work_approval_deadline_ms": int64(1)}
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Confirm the precondition: BlockingStep() returns nil for this interrupt pause.
	if gate := wf.BlockingStep(); gate != nil {
		t.Fatalf("precondition: BlockingStep() = %s, want nil for interrupt pause", gate.ID)
	}

	engine.cancelExpiredApprovals()

	loaded, _ := store.Load("wf-interrupt")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval (interrupt pause is not an approval gate)", loaded.State)
	}
}

// TestApprovalTimeout_ReopenClearsStaleDeadline_NotReCancelled is the
// regression test for issue #25 bug 1 (BLOCKER, deterministic): when a workflow
// times out and is auto-cancelled by cancelExpiredApprovals, its
// Context[gate.ID+"_approval_deadline_ms"] key is left behind (Cancel never
// deletes it). A human then calls Reopen to look at it — the workflow goes back
// to StateWaitingApproval, BlockingStep() resolves to the SAME gate, and its
// OLD expired deadline key is still sitting there. Without the fix, the very
// next watchdog tick (cancelExpiredApprovals) finds that same gate with the
// same already-past deadline and cancels the workflow AGAIN — before the human
// has any real chance to act, repeating every tick. The fix makes Reopen's
// store.Modify closure clear the stale deadline. This test drives the timeout
// -> cancel path the same way TestApprovalTimeout_ExpiredDeadline_Cancelled
// does, then calls Reopen, then calls cancelExpiredApprovals() again
// (simulating the next watchdog tick) and asserts the workflow is STILL
// StateWaitingApproval, NOT re-cancelled. Falsification: if Reopen's closure
// does not delete the deadline key, the second cancelExpiredApprovals() call
// re-cancels and this test fails.
func TestApprovalTimeout_ReopenClearsStaleDeadline_NotReCancelled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	past := time.Now().Add(-time.Hour).UnixMilli()
	wf := NewWorkflow("wf-reopen-timeout", "ReopenTimeout", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	wf.Context = map[string]any{"gate_approval_deadline_ms": int64(past)}
	if err := store.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// First watchdog tick: the expired deadline auto-cancels the workflow.
	engine.cancelExpiredApprovals()
	loaded, _ := store.Load("wf-reopen-timeout")
	if loaded.State != StateCancelled {
		t.Fatalf("after first tick: state = %s, want cancelled (deadline expired)", loaded.State)
	}
	// The stale deadline key is still present after auto-cancel (Cancel never
	// deletes it) — this is the precondition for the bug.
	if _, ok := loaded.Context["gate_approval_deadline_ms"]; !ok {
		t.Fatal("precondition: stale gate_approval_deadline_ms must still be present after auto-cancel")
	}

	// Human reopens the cancelled workflow to look at it.
	if err := engine.Reopen("wf-reopen-timeout"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	loaded, _ = store.Load("wf-reopen-timeout")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("after reopen: state = %s, want waiting_approval", loaded.State)
	}
	// The fix: Reopen must have cleared the stale deadline key.
	if _, ok := loaded.Context["gate_approval_deadline_ms"]; ok {
		t.Fatal("after reopen: stale gate_approval_deadline_ms should have been cleared by Reopen")
	}

	// Second watchdog tick (simulated): the workflow must NOT be re-cancelled.
	engine.cancelExpiredApprovals()
	loaded, _ = store.Load("wf-reopen-timeout")
	if loaded.State != StateWaitingApproval {
		t.Errorf("after second tick: state = %s, want waiting_approval (reopened workflow must not be re-cancelled by stale deadline)", loaded.State)
	}
}

// TestApprovalTimeout_ConcurrentlyApproved_NotCancelled is the regression test
// for issue #25 bug 2 (MAJOR, real race window): cancelExpiredApprovals decides
// which workflows to cancel based on a stale store.List(StateWaitingApproval)
// snapshot, then mutates. If a human calls HandleApproval in the window between
// the List() snapshot and the actual mutation, the workflow has already moved
// to StateRunning, its deadline key was already deleted, and ResumeAsync may
// already be executing downstream steps — yet the old code called the public
// Cancel, which only refuses terminal workflows and would cancel the
// now-Running workflow anyway, silently undoing a legitimate approval. The fix
// re-checks state inside one atomic store.Modify closure against FRESH state.
//
// This test faithfully simulates the TOCTOU window with a staleSnapshotBackend:
// it captures the List(StateWaitingApproval) snapshot BEFORE the concurrent
// approval, then wraps the backend so List keeps returning that stale snapshot
// (still WaitingApproval with an expired deadline) while Modify operates
// against the LIVE backend (which a concurrent HandleApproval has already moved
// to StateRunning, deadline key deleted). cancelExpiredApprovals() then runs
// against the stale snapshot but its in-Modify re-check sees the live
// StateRunning and must refuse to cancel. Falsification: if
// cancelExpiredApprovals mutates based on the stale snapshot without a fresh
// in-Modify re-check (e.g. calls the public Cancel, which only refuses
// terminal states), it cancels the already-approved workflow and this test
// fails.
func TestApprovalTimeout_ConcurrentlyApproved_NotCancelled(t *testing.T) {
	t.Parallel()

	inner := &memBackend{wf: make(map[string]*Workflow)}

	past := time.Now().Add(-time.Hour).UnixMilli()
	wf := NewWorkflow("wf-race", "Race", "test", []Step{
		{ID: "gate", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	wf.CurrentStep = "gate"
	wf.State = StateWaitingApproval
	wf.Context = map[string]any{"gate_approval_deadline_ms": int64(past)}
	if err := inner.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Capture the stale snapshot the watchdog's List() will see — taken BEFORE
	// the concurrent approval moves the live state on.
	staleSnap := inner.List(StateWaitingApproval)
	if len(staleSnap) != 1 || staleSnap[0].ID != "wf-race" {
		t.Fatalf("stale snapshot setup: got %d workflows, want 1 (wf-race)", len(staleSnap))
	}

	// Wrap so List(StateWaitingApproval) keeps returning the stale snapshot,
	// while Load/Modify operate against the live inner backend.
	backend := &staleSnapshotBackend{
		inner: inner,
		stale: map[WorkflowState][]*Workflow{StateWaitingApproval: staleSnap},
	}
	store := NewWorkflowStore(backend)
	engine := NewEngine(store)

	// Simulate a concurrent HandleApproval resolving the gate in the window
	// between the List() snapshot above and the Modify below: the live
	// workflow transitions to StateRunning and the deadline key is deleted,
	// exactly as HandleApproval's normal-resolve path does.
	if err := store.Modify("wf-race", func(w *Workflow) {
		gate := w.GetStep("gate")
		if gate != nil && gate.Kind == StepApproval && gate.State == StepPending {
			gate.State = StepCompleted
			gate.Result = approvalResult
			gate.EndedAt = time.Now().UnixMilli()
			w.Context[gate.ID] = approvalResult
			delete(w.Context, gate.ID+"_approval_deadline_ms")
		}
		w.State = StateRunning
		w.UpdatedAt = time.Now().UnixMilli()
	}); err != nil {
		t.Fatalf("simulate concurrent approval: %v", err)
	}
	loaded, _ := store.Load("wf-race")
	if loaded.State != StateRunning {
		t.Fatalf("precondition: live state = %s, want running (concurrent approval)", loaded.State)
	}

	// Watchdog tick: store.List(StateWaitingApproval) returns the STALE snapshot
	// (still WaitingApproval with an expired deadline), but the in-Modify
	// re-check must see the live StateRunning and refuse to cancel.
	engine.cancelExpiredApprovals()

	loaded, _ = store.Load("wf-race")
	if loaded.State != StateRunning {
		t.Errorf("state = %s, want running (concurrent approval must not be undone by stale-snapshot cancel)", loaded.State)
	}
	if loaded.GetStep("gate").State != StepCompleted {
		t.Errorf("gate step state = %s, want completed (concurrent approval must not be undone)", loaded.GetStep("gate").State)
	}
}

// staleSnapshotBackend wraps a StoreBackend so that List returns a pre-captured
// stale snapshot for given states, while every other method (notably Modify and
// Load) delegates to the live inner backend. This lets a single-threaded test
// faithfully simulate the TOCTOU window in cancelExpiredApprovals: the
// watchdog's List() sees a snapshot taken BEFORE a concurrent mutation, but the
// Modify closure observes the live, already-mutated state.
type staleSnapshotBackend struct {
	inner StoreBackend
	stale map[WorkflowState][]*Workflow
}

func (s *staleSnapshotBackend) Save(w *Workflow) error                  { return s.inner.Save(w) }
func (s *staleSnapshotBackend) Load(id string) (*Workflow, bool)        { return s.inner.Load(id) }
func (s *staleSnapshotBackend) Delete(id string) error                  { return s.inner.Delete(id) }
func (s *staleSnapshotBackend) ListByOwner(owner string) []*Workflow    { return s.inner.ListByOwner(owner) }
func (s *staleSnapshotBackend) FindByIdempotencyKey(key string) *Workflow {
	return s.inner.FindByIdempotencyKey(key)
}
func (s *staleSnapshotBackend) Modify(id string, fn func(w *Workflow)) error {
	return s.inner.Modify(id, fn)
}
func (s *staleSnapshotBackend) Close() error { return s.inner.Close() }

func (s *staleSnapshotBackend) List(state WorkflowState) []*Workflow {
	if snap, ok := s.stale[state]; ok {
		return snap
	}
	return s.inner.List(state)
}
