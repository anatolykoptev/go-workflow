package workflow

import (
	"context"
	"testing"
)

// TestRateLimit_ThrottledStepRetries verifies that a throttled step is routed
// through handleStepError (retry machinery), NOT hard-failed with an early return.
//
// Red-on-revert evidence: without the execErr=limitErr fix in engine_step.go,
// step.Retries stays 0 (handleStepError never called). The test asserts Retries >= 1.
//
// Setup: rate=0.001/s burst=1 — the pre-exhaust call takes the sole burst token.
// Refill at 0.001 tps takes ~1000s, so every attempt within the test window is
// throttled. max=2 retries → step dead-letters after 3 throttled attempts (attempt
// 1 + 2 retries), each incrementing Retries.
func TestRateLimit_ThrottledStepRetries(t *testing.T) {
	t.Parallel()

	runner := &mockToolRunner{results: map[string]string{"my-tool": "ok"}}
	store := newTestStore(t)
	engine := NewEngine(store,
		WithToolRunner(runner),
		// rate=0.001/s, burst=1: one immediate allow, then ~1000s until refill.
		// After we pre-exhaust the burst below, every step attempt is throttled.
		WithRateLimit("my-tool", 0.001, 1),
	)

	// Pre-exhaust the burst token so the very first step attempt is throttled.
	if err := engine.rateLimits.check("my-tool"); err != nil {
		t.Fatalf("pre-exhaust failed unexpectedly: %v", err)
	}

	wf := NewWorkflow("wf-rl-retry", "RateLimitRetry", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "my-tool",
			"retry": map[string]any{
				"max":      float64(2),
				"delay_ms": float64(1),
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)

	// Engine returns an error (workflow failed due to dead-letter).
	_ = engine.Start(context.Background(), "wf-rl-retry")

	loaded, _ := store.Load("wf-rl-retry")
	s := loaded.GetStep("s1")

	// Step must be dead-lettered (all 3 attempts throttled).
	if s.State != StepDeadLettered {
		t.Errorf("step state = %s, want dead_lettered", s.State)
	}
	// Retries >= 1 proves throttle went through handleStepError.
	// With the bare `return limitErr` bug, Retries == 0 (handler never reached).
	if s.Retries < 1 {
		t.Errorf("step.Retries = %d, want >= 1; throttle error must route through handleStepError", s.Retries)
	}
}
