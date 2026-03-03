package workflow

import (
	"context"
	"testing"
	"time"
)

func TestSuspend_Hard_MissingSuspendUntilMS(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf1", "NoDeadline", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err == nil {
		t.Fatal("expected error for missing suspend_until_ms")
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateFailed {
		t.Errorf("state = %s, want failed", loaded.State)
	}
}

func TestSuspend_Hard_ZeroDeadline(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf1", "ZeroDeadline", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": float64(0),
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err == nil {
		t.Fatal("expected error for zero suspend_until_ms")
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateFailed {
		t.Errorf("state = %s, want failed", loaded.State)
	}
}

func TestSuspend_Hard_NegativeDeadline(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf1", "NegDeadline", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": float64(-1000),
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err == nil {
		t.Fatal("expected error for negative suspend_until_ms")
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateFailed {
		t.Errorf("state = %s, want failed", loaded.State)
	}
}

func TestSuspend_Hard_NonNumericDeadline(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	for _, val := range []any{"tomorrow", true, nil, []int{1}} {
		wf := NewWorkflow("wf1", "BadType", "test", []Step{
			{ID: "wait", Kind: StepSuspend, Config: map[string]any{
				"suspend_until_ms": val,
			}, State: StepPending},
		})
		_ = store.Save(wf)

		err := engine.Start(context.Background(), "wf1")
		if err == nil {
			t.Errorf("expected error for suspend_until_ms=%v (%T)", val, val)
		}
		_ = store.Delete("wf1")
	}
}

func TestSuspend_Hard_FarFutureDeadline(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	// Year 2099 deadline — must pause, not overflow
	farFuture := float64(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	wf := NewWorkflow("wf1", "FarFuture", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": farFuture,
		}, State: StepPending},
	})
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StatePaused {
		t.Errorf("state = %s, want paused for far-future deadline", loaded.State)
	}
	// Watchdog should NOT resume this
	engine.resumeSuspended()
	loaded, _ = store.Load("wf1")
	if loaded.State != StatePaused {
		t.Errorf("state = %s after watchdog, want still paused", loaded.State)
	}
}

func TestSuspend_Hard_WatchdogIgnoresNonSuspendPaused(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store)

	// Workflow paused by interrupt_before (no suspend context) — watchdog must NOT resume
	wf := NewWorkflow("wf1", "InterruptPaused", "test", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "x"}, State: StepPending},
	})
	wf.State = StatePaused
	// No _suspend_until_ms in context
	_ = store.Save(wf)

	engine.resumeSuspended()

	loaded, _ := store.Load("wf1")
	if loaded.State != StatePaused {
		t.Errorf("state = %s, want paused (watchdog should ignore non-suspend paused)", loaded.State)
	}
}

func TestSuspend_Hard_WatchdogSelectiveResume(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{"after": "done"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	// wf1: expired deadline — should resume
	wf1 := NewWorkflow("wf1", "Expired", "test", []Step{
		{ID: "wait", Kind: StepSuspend, State: StepCompleted},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "after"},
			DependsOn: []string{"wait"}, State: StepPending},
	})
	wf1.State = StatePaused
	wf1.Context["wait_suspend_until_ms"] = int64(1000)
	_ = store.Save(wf1)

	// wf2: future deadline — should NOT resume
	future := time.Now().Add(time.Hour).UnixMilli()
	wf2 := NewWorkflow("wf2", "Future", "test", []Step{
		{ID: "wait", Kind: StepSuspend, State: StepCompleted},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "after"},
			DependsOn: []string{"wait"}, State: StepPending},
	})
	wf2.State = StatePaused
	wf2.Context["wait_suspend_until_ms"] = float64(future)
	_ = store.Save(wf2)

	engine.resumeSuspended()

	// Wait for async resume of wf1
	for i := 0; i < 50; i++ {
		l, _ := store.Load("wf1")
		if l.State == StateCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	loaded1, _ := store.Load("wf1")
	if loaded1.State != StateCompleted {
		t.Errorf("wf1 state = %s, want completed (expired deadline)", loaded1.State)
	}

	loaded2, _ := store.Load("wf2")
	if loaded2.State != StatePaused {
		t.Errorf("wf2 state = %s, want paused (future deadline)", loaded2.State)
	}
}

func TestSuspend_Hard_ContextPreservedAcrossResume(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{"setup": "data123", "after": "final"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	past := float64(time.Now().Add(-time.Hour).UnixMilli())
	wf := NewWorkflow("wf1", "ContextKeep", "test", []Step{
		{ID: "setup", Kind: StepTool, Config: map[string]any{"tool": "setup"}, State: StepPending},
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": past,
		}, DependsOn: []string{"setup"}, State: StepPending},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "after"},
			DependsOn: []string{"wait"}, State: StepPending},
	})
	_ = store.Save(wf)

	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s, want completed", loaded.State)
	}
	// setup step result should be preserved in context
	if loaded.Context["setup"] != "data123" {
		t.Errorf("context[setup] = %v, want data123", loaded.Context["setup"])
	}
}

func TestFindSuspendDeadline_Hard_MultipleKeys(t *testing.T) {
	t.Parallel()
	wf := &Workflow{Context: map[string]any{
		"step1_suspend_until_ms": int64(1000),
		"step2_suspend_until_ms": float64(2000),
		"unrelated_key":          "hello",
	}}

	deadline := findSuspendDeadline(wf)
	// Should find one of the two deadlines (both > 0)
	if deadline != 1000 && deadline != 2000 {
		t.Errorf("deadline = %d, want 1000 or 2000", deadline)
	}
}

func TestFindSuspendDeadline_Hard_ZeroAndNegativeIgnored(t *testing.T) {
	t.Parallel()
	wf := &Workflow{Context: map[string]any{
		"bad_suspend_until_ms":  int64(0),
		"neg_suspend_until_ms":  float64(-500),
		"good_suspend_until_ms": int64(5000),
	}}

	deadline := findSuspendDeadline(wf)
	if deadline != 5000 {
		t.Errorf("deadline = %d, want 5000 (zero/negative ignored)", deadline)
	}
}

func TestFindSuspendDeadline_Hard_NoMatchingKeys(t *testing.T) {
	t.Parallel()
	wf := &Workflow{Context: map[string]any{
		"suspend_until":       int64(1000), // wrong suffix
		"something_ms":        float64(999),
		"suspend_until_ms_v2": int64(1000), // wrong suffix
	}}

	deadline := findSuspendDeadline(wf)
	if deadline != 0 {
		t.Errorf("deadline = %d, want 0 (no matching keys)", deadline)
	}
}

func TestFindSuspendDeadline_Hard_EmptyContext(t *testing.T) {
	t.Parallel()
	wf := &Workflow{Context: map[string]any{}}
	if d := findSuspendDeadline(wf); d != 0 {
		t.Errorf("deadline = %d, want 0", d)
	}
	wf2 := &Workflow{}
	if d := findSuspendDeadline(wf2); d != 0 {
		t.Errorf("nil context: deadline = %d, want 0", d)
	}
}
