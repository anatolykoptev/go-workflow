package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- Exponential backoff ---

func TestExponentialBackoff(t *testing.T) {
	runner := &mockToolRunner{err: errors.New("transient error")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Backoff", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "fail_tool",
			"retry": map[string]any{
				"max":                float64(3),
				"delay_ms":           float64(10),
				"backoff_multiplier": float64(2.0),
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)

	start := time.Now()
	_ = engine.Start(context.Background(), "wf1")
	elapsed := time.Since(start)

	loaded, _ := store.Load("wf1")
	s := loaded.GetStep("s1")
	if s.Retries != 3 {
		t.Errorf("retries = %d, want 3", s.Retries)
	}

	// Delays: 10ms (mult^0=1x), 20ms (mult^1=2x), 40ms (mult^2=4x) = 70ms minimum
	if elapsed < 60*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 60ms (exponential backoff)", elapsed)
	}
}

func TestBackoffCapped(t *testing.T) {
	runner := &mockToolRunner{err: errors.New("fail")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Capped", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "fail_tool",
			"retry": map[string]any{
				"max":                float64(3),
				"delay_ms":           float64(10),
				"backoff_multiplier": float64(10.0),
				"max_delay_ms":       float64(25),
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)

	start := time.Now()
	_ = engine.Start(context.Background(), "wf1")
	elapsed := time.Since(start)

	// Without cap: 10, 100, 1000ms = 1110ms total delay
	// With cap at 25: 10, 25, 25 = 60ms delay (plus store I/O overhead)
	// Should be well under 1s (proof the cap is working vs 1110ms uncapped)
	if elapsed > 1*time.Second {
		t.Errorf("elapsed = %v, want < 1s (backoff should be capped at 25ms, not 1000ms)", elapsed)
	}
}

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		name       string
		baseMS     int64
		attempt    int
		multiplier float64
		maxMS      int64
		want       int64
	}{
		{"no backoff (mult=1)", 100, 1, 1.0, 0, 100},
		{"no backoff (mult=0)", 100, 3, 0.5, 0, 100},
		{"first attempt (mult^0)", 100, 1, 2.0, 0, 100},
		{"second attempt (mult^1)", 100, 2, 2.0, 0, 200},
		{"third attempt (mult^2)", 100, 3, 2.0, 0, 400},
		{"capped", 100, 5, 2.0, 500, 500},
		{"not capped", 100, 2, 2.0, 500, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateBackoff(tt.baseMS, tt.attempt, tt.multiplier, tt.maxMS)
			if got != tt.want {
				t.Errorf("calculateBackoff(%d, %d, %.1f, %d) = %d, want %d",
					tt.baseMS, tt.attempt, tt.multiplier, tt.maxMS, got, tt.want)
			}
		})
	}
}

// --- Per-step timeout ---

func TestPerStepTimeout(t *testing.T) {
	runner := &slowToolRunner{delay: 500 * time.Millisecond}
	store := newTestStore(t)
	executors := map[StepKind]StepExecutor{
		StepTool: NewToolExecutor(runner),
	}
	engine := &Engine{store: store, executors: executors}

	wf := NewWorkflow("wf1", "Timeout", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool":       "slow_tool",
			"timeout_ms": float64(50),
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error = %q, want context deadline exceeded", err.Error())
	}
}

func TestPerStepTimeoutFromPolicy(t *testing.T) {
	runner := &slowToolRunner{delay: 500 * time.Millisecond}
	store := newTestStore(t)
	executors := map[StepKind]StepExecutor{
		StepTool: NewToolExecutor(runner),
	}
	engine := &Engine{store: store, executors: executors}

	wf := NewWorkflow("wf1", "PolicyTimeout", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "slow_tool",
		}, State: StepPending},
	})
	wf.Security = &SecurityPolicy{MaxStepDuration: 50 * time.Millisecond}
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err == nil {
		t.Fatal("expected timeout error from SecurityPolicy.MaxStepDuration")
	}
}

// --- Dead letter ---

func TestStepDeadLettered(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{err: errors.New("permanent failure")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "DeadLetter", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "fail_tool",
			"retry": map[string]any{
				"max":      float64(2),
				"delay_ms": float64(1),
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	s := loaded.GetStep("s1")
	if s.State != StepDeadLettered {
		t.Errorf("step state = %s, want dead_lettered", s.State)
	}
	if loaded.State != StateFailed {
		t.Errorf("workflow state = %s, want failed", loaded.State)
	}
	if !strings.Contains(loaded.Error, "dead-lettered") {
		t.Errorf("workflow error = %q, want 'dead-lettered'", loaded.Error)
	}
	if GlobalMetrics.StepsDeadLettered.Load() != 1 {
		t.Errorf("StepsDeadLettered = %d, want 1", GlobalMetrics.StepsDeadLettered.Load())
	}
}

func TestDeadLetteredNotAutoRetried(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "DeadNoRetry", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepDeadLettered,
			Error: "timeout", Retries: 3},
	})
	wf.State = StateFailed
	wf.Error = "step s1 dead-lettered: timeout"
	wf.UpdatedAt = time.Now().UnixMilli()
	_ = store.Save(wf)

	retried := engine.AutoRetryFailed(time.Hour)
	if retried != 0 {
		t.Errorf("retried = %d, want 0 (dead-lettered steps should not be retried)", retried)
	}
}

// --- Conditional retry ---

func TestRetryOnPattern(t *testing.T) {
	runner := &mockToolRunner{err: errors.New("permanent: invalid input")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "RetryOn", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "t",
			"retry": map[string]any{
				"max":      float64(3),
				"delay_ms": float64(1),
				"retry_on": []any{"timeout", "rate limit"},
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	s := loaded.GetStep("s1")
	if s.Retries != 0 {
		t.Errorf("retries = %d, want 0 (error doesn't match retry_on patterns)", s.Retries)
	}
}

func TestSkipOnPattern(t *testing.T) {
	runner := &mockToolRunner{err: errors.New("permanent error: do not retry")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "SkipOn", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{
			"tool": "t",
			"retry": map[string]any{
				"max":      float64(3),
				"delay_ms": float64(1),
				"skip_on":  []any{"permanent"},
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	s := loaded.GetStep("s1")
	if s.Retries != 0 {
		t.Errorf("retries = %d, want 0 (error matches skip_on pattern)", s.Retries)
	}
}

// --- Idempotency ---

func TestIdempotencyKey(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"t": "ok"}}
	engine, store := newTestEngine(t, runner)

	wf1 := NewWorkflow("wf1", "Idemp1", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
	})
	wf1.IdempotencyKey = "order-123"
	_ = store.Save(wf1)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("first start failed: %v", err)
	}

	// Set first back to running to simulate active workflow
	_ = store.Modify("wf1", func(w *Workflow) {
		w.State = StateRunning
	})

	wf2 := NewWorkflow("wf2", "Idemp2", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
	})
	wf2.IdempotencyKey = "order-123"
	_ = store.Save(wf2)

	err = engine.Start(context.Background(), "wf2")
	if err == nil {
		t.Fatal("expected duplicate idempotency key error")
	}
	if !strings.Contains(err.Error(), "duplicate idempotency key") {
		t.Errorf("error = %q, want 'duplicate idempotency key'", err.Error())
	}
}

func TestIdempotencyKeyAllowsTerminal(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"t": "ok"}}
	engine, store := newTestEngine(t, runner)

	wf1 := NewWorkflow("wf1", "Idemp1", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
	})
	wf1.IdempotencyKey = "order-456"
	_ = store.Save(wf1)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Fatalf("wf1 state = %s, want completed", loaded.State)
	}

	wf2 := NewWorkflow("wf2", "Idemp2", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
	})
	wf2.IdempotencyKey = "order-456"
	_ = store.Save(wf2)

	err := engine.Start(context.Background(), "wf2")
	if err != nil {
		t.Fatalf("expected success (first workflow is terminal), got: %v", err)
	}
}
