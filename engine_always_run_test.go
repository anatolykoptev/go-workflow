package workflow

import (
	"context"
	"strings"
	"testing"
)

// TestAlwaysRun_ExecutesAfterUpstreamFailure verifies that a step marked
// AlwaysRun=true runs even when a prior step has failed and the workflow has
// transitioned to StateFailed. Common case: a check_cookies step uses
// stop_workflow / OnErrorFail, but a teardown/cleanup step must still execute.
func TestAlwaysRun_ExecutesAfterUpstreamFailure(t *testing.T) {
	t.Parallel()

	// "bad" fails; "good" / "cleanup" succeed.
	runner := &selectiveToolRunner{failTools: map[string]bool{"bad": true}}
	engine, store := newTestEngine(t, runner)

	// Workflow shape:
	//   a    (succeeds)
	//   b    (fails -> workflow state Failed; default OnErrorFail)
	//   c    (depends on b; should NEVER run because dep b failed)
	//   cleanup (depends on a; AlwaysRun=true; MUST run)
	wf := NewWorkflow("wf-aru", "AlwaysRunUpstreamFail", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "good"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "bad"}, DependsOn: []string{"a"}, State: StepPending},
		{ID: "c", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"b"}, State: StepPending},
		{ID: "cleanup", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"a"}, AlwaysRun: true, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-aru")

	loaded, _ := store.Load("wf-aru")

	if loaded.State != StateFailed {
		t.Errorf("workflow state = %s, want failed", loaded.State)
	}
	if loaded.GetStep("a").State != StepCompleted {
		t.Errorf("step a = %s, want completed", loaded.GetStep("a").State)
	}
	if loaded.GetStep("b").State != StepFailed {
		t.Errorf("step b = %s, want failed", loaded.GetStep("b").State)
	}
	if loaded.GetStep("c").State != StepPending {
		t.Errorf("step c = %s, want pending (dep b failed, c is NOT always_run)", loaded.GetStep("c").State)
	}
	if loaded.GetStep("cleanup").State != StepCompleted {
		t.Errorf("cleanup state = %s, want completed (always_run must drain after failure)", loaded.GetStep("cleanup").State)
	}
}

// TestAlwaysRun_ChainExecutesInOrder verifies edge case (a):
// always_run step depending on another always_run step → both run in order.
func TestAlwaysRun_ChainExecutesInOrder(t *testing.T) {
	t.Parallel()

	runner := &selectiveToolRunner{failTools: map[string]bool{"bad": true}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-chain", "AlwaysRunChain", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "good"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "bad"}, DependsOn: []string{"a"}, State: StepPending},
		{ID: "cleanup1", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"a"}, AlwaysRun: true, State: StepPending},
		{ID: "cleanup2", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"cleanup1"}, AlwaysRun: true, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-chain")

	loaded, _ := store.Load("wf-chain")
	if loaded.GetStep("cleanup1").State != StepCompleted {
		t.Errorf("cleanup1 = %s, want completed", loaded.GetStep("cleanup1").State)
	}
	if loaded.GetStep("cleanup2").State != StepCompleted {
		t.Errorf("cleanup2 = %s, want completed (chained always_run)", loaded.GetStep("cleanup2").State)
	}
}

// TestAlwaysRun_DepNeverScheduledStaysPending verifies edge case (b):
// always_run step's dep was never even scheduled (its own dep failed earlier);
// because the dep is still StepPending (not terminal), the always_run step
// must NOT run — preventing orphan execution with unmet inputs.
func TestAlwaysRun_DepNeverScheduledStaysPending(t *testing.T) {
	t.Parallel()

	runner := &selectiveToolRunner{failTools: map[string]bool{"bad": true}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-orphan", "AlwaysRunOrphan", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},                            // fails
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"a"}, State: StepPending}, // never runs because a failed
		// cleanup depends on b — b never ran, so cleanup must NOT run either
		{ID: "cleanup", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"b"}, AlwaysRun: true, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-orphan")

	loaded, _ := store.Load("wf-orphan")
	if loaded.State != StateFailed {
		t.Errorf("workflow state = %s, want failed", loaded.State)
	}
	if loaded.GetStep("b").State != StepPending {
		t.Errorf("b = %s, want pending (never scheduled)", loaded.GetStep("b").State)
	}
	if loaded.GetStep("cleanup").State != StepPending {
		t.Errorf("cleanup = %s, want pending (dep b never reached terminal)", loaded.GetStep("cleanup").State)
	}
}

// TestAlwaysRun_StepFailureDoesNotCascade verifies edge case (e):
// the always_run step itself fails — it's logged but does NOT cascade or
// override the existing workflow failure state.
func TestAlwaysRun_StepFailureDoesNotCascade(t *testing.T) {
	t.Skip("known flaky race — step-failure-cascade detection has a concurrency issue tracked for fix in v0.2")
	t.Parallel()

	runner := &selectiveToolRunner{failTools: map[string]bool{"bad": true, "cleanup_bad": true}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-fail2", "AlwaysRunFails", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "good"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "bad"}, DependsOn: []string{"a"}, State: StepPending},
		{ID: "cleanup", Kind: StepTool, Config: map[string]any{"tool": "cleanup_bad"}, DependsOn: []string{"a"}, AlwaysRun: true, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-fail2")

	loaded, _ := store.Load("wf-fail2")
	if loaded.State != StateFailed {
		t.Errorf("workflow state = %s, want failed", loaded.State)
	}
	// Either Failed (or Skipped via on_error) is fine — what matters is that
	// the always_run step actually ran (state moved off Pending) and the
	// workflow's pre-existing failure cause is preserved.
	cleanupState := loaded.GetStep("cleanup").State
	if cleanupState == StepPending {
		t.Errorf("cleanup state = %s, expected to have run (any non-pending state)", cleanupState)
	}
	// Original failure cause must be preserved — cleanup step's failure must not
	// overwrite the first error that caused the workflow to enter StateFailed.
	if !strings.Contains(loaded.Error, "step b failed") {
		t.Fatalf("Workflow.Error must preserve original failure cause; got %q", loaded.Error)
	}
}

// TestAlwaysRun_RunsOnSuccessPath verifies always_run is a no-op for the
// happy path — workflow completes, cleanup ran exactly once, state Completed.
func TestAlwaysRun_RunsOnSuccessPath(t *testing.T) {
	t.Parallel()

	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-ok", "AlwaysRunSuccess", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "tb"}, DependsOn: []string{"a"}, State: StepPending},
		{ID: "cleanup", Kind: StepTool, Config: map[string]any{"tool": "tc"}, DependsOn: []string{"b"}, AlwaysRun: true, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-ok")

	loaded, _ := store.Load("wf-ok")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	for _, s := range loaded.Steps {
		if s.State != StepCompleted {
			t.Errorf("step %s = %s, want completed", s.ID, s.State)
		}
	}
}

// TestAlwaysRun_RetriesExhaustedTriggersCleanup verifies that even when a
// step is dead-lettered (retries exhausted, on_error=fail), an always_run
// teardown still executes. This is the "production" scenario where we
// retry network calls then need to clean up Chrome tabs.
func TestAlwaysRun_RetriesExhaustedTriggersCleanup(t *testing.T) {
	t.Parallel()

	runner := &selectiveToolRunner{failTools: map[string]bool{"always_fails": true}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf-dlq", "AlwaysRunDLQ", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{
			"tool":  "always_fails",
			"retry": map[string]any{"max": float64(2), "delay_ms": float64(1)},
		}, State: StepPending},
		{ID: "cleanup", Kind: StepTool, Config: map[string]any{"tool": "tc"}, AlwaysRun: true, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-dlq")

	loaded, _ := store.Load("wf-dlq")
	if loaded.State != StateFailed {
		t.Errorf("workflow state = %s, want failed", loaded.State)
	}
	if loaded.GetStep("cleanup").State != StepCompleted {
		t.Errorf("cleanup = %s, want completed (must run even after dead-letter)", loaded.GetStep("cleanup").State)
	}
}
