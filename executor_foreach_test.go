package workflow

import (
	"context"
	"testing"
	"time"
)

func TestForEachExecutor_Parallel(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"process": "done"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	wf := NewWorkflow("wf1", "ForEach", "test", []Step{
		{ID: "produce", Kind: StepTool, Config: map[string]any{"tool": "produce"}, State: StepPending},
		{ID: "loop", Kind: StepForEach, Config: map[string]any{
			"items":       "produce",
			"step_kind":   "tool",
			"step_config": map[string]any{"tool": "process"},
		}, DependsOn: []string{"produce"}, State: StepPending},
	})
	wf.Context["produce"] = []any{"a", "b", "c"}
	_ = store.Save(wf)

	_ = store.Modify("wf1", func(w *Workflow) {
		w.GetStep("produce").State = StepCompleted
		w.GetStep("produce").Result = "ok"
	})

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}

	// ForEach should have injected 3 child steps
	count := 0
	for _, s := range loaded.Steps {
		if s.ID == "loop_0" || s.ID == "loop_1" || s.ID == "loop_2" {
			count++
			if s.State != StepCompleted {
				t.Errorf("child %s state = %s, want completed", s.ID, s.State)
			}
		}
	}
	if count != 3 {
		t.Errorf("child steps = %d, want 3", count)
	}
}

func TestForEachExecutor_Sequential(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"process": "done"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	wf := NewWorkflow("wf1", "ForEachSeq", "test", []Step{
		{ID: "loop", Kind: StepForEach, Config: map[string]any{
			"items":       "data",
			"step_kind":   "tool",
			"step_config": map[string]any{"tool": "process"},
			"sequential":  true,
		}, State: StepPending},
	})
	wf.Context["data"] = []any{"x", "y"}
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	// Sequential: loop_1 depends on loop_0
	s1 := loaded.GetStep("loop_1")
	if s1 == nil {
		t.Fatal("loop_1 not found")
	}
	if len(s1.DependsOn) != 1 || s1.DependsOn[0] != "loop_0" {
		t.Errorf("loop_1.DependsOn = %v, want [loop_0]", s1.DependsOn)
	}
}

func TestForEachExecutor_EmptyList(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf1", "EmptyForEach", "test", []Step{
		{ID: "loop", Kind: StepForEach, Config: map[string]any{
			"items":       "data",
			"step_kind":   "tool",
			"step_config": map[string]any{"tool": "noop"},
		}, State: StepPending},
	})
	wf.Context["data"] = []any{}
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

func TestForEachExecutor_Concurrency(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"process": "done"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	wf := NewWorkflow("wf1", "ForEachConc", "test", []Step{
		{ID: "loop", Kind: StepForEach, Config: map[string]any{
			"items":       "data",
			"step_kind":   "tool",
			"step_config": map[string]any{"tool": "process"},
			"concurrency": float64(2),
		}, State: StepPending},
	})
	// 5 items with concurrency=2
	wf.Context["data"] = []any{"a", "b", "c", "d", "e"}
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")

	// loop_0, loop_1 should have no dependencies
	s0 := loaded.GetStep("loop_0")
	if len(s0.DependsOn) != 0 {
		t.Errorf("loop_0.DependsOn = %v, want []", s0.DependsOn)
	}
	s1 := loaded.GetStep("loop_1")
	if len(s1.DependsOn) != 0 {
		t.Errorf("loop_1.DependsOn = %v, want []", s1.DependsOn)
	}

	// loop_2 depends on loop_0
	s2 := loaded.GetStep("loop_2")
	if len(s2.DependsOn) != 1 || s2.DependsOn[0] != "loop_0" {
		t.Errorf("loop_2.DependsOn = %v, want [loop_0]", s2.DependsOn)
	}

	// loop_4 depends on loop_2
	s4 := loaded.GetStep("loop_4")
	if len(s4.DependsOn) != 1 || s4.DependsOn[0] != "loop_2" {
		t.Errorf("loop_4.DependsOn = %v, want [loop_2]", s4.DependsOn)
	}
}

func TestBranchAllExecutor(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"fetch": "data", "analyze": "result"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	wf := NewWorkflow("wf1", "BranchAll", "test", []Step{
		{ID: "fanout", Kind: StepBranchAll, Config: map[string]any{
			"branches": []any{
				map[string]any{"id": "b1", "kind": "tool", "config": map[string]any{"tool": "fetch"}},
				map[string]any{"id": "b2", "kind": "tool", "config": map[string]any{"tool": "analyze"}},
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}

	b1 := loaded.GetStep("b1")
	b2 := loaded.GetStep("b2")
	if b1 == nil || b2 == nil {
		t.Fatal("branch steps not found")
	}
	if b1.State != StepCompleted || b2.State != StepCompleted {
		t.Errorf("branch states: b1=%s, b2=%s", b1.State, b2.State)
	}
}

func TestBranchAllExecutor_Empty(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf1", "EmptyBranch", "test", []Step{
		{ID: "fanout", Kind: StepBranchAll, Config: map[string]any{
			"branches": []any{},
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s", loaded.State)
	}
}

func TestSuspendExecutor_PastDeadline(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store)

	past := float64(time.Now().Add(-time.Hour).UnixMilli())
	wf := NewWorkflow("wf1", "PastSuspend", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": past,
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed (deadline already passed)", loaded.State)
	}
}

func TestSuspendExecutor_FutureDeadline(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store)

	future := float64(time.Now().Add(time.Hour).UnixMilli())
	wf := NewWorkflow("wf1", "FutureSuspend", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": future,
		}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.Start(context.Background(), "wf1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StatePaused {
		t.Errorf("state = %s, want paused", loaded.State)
	}
}

func TestInjectSteps(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store)

	wf := NewWorkflow("wf1", "Inject", "test", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "x"}, State: StepPending},
		{ID: "c", Kind: StepTool, Config: map[string]any{"tool": "z"}, State: StepPending},
	})
	_ = store.Save(wf)

	err := engine.InjectStepsAndRewriteDeps("wf1", []Step{
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "y"}, State: StepPending},
	}, "a", "")
	if err != nil {
		t.Fatalf("InjectStepsAndRewriteDeps: %v", err)
	}

	loaded, _ := store.Load("wf1")
	if len(loaded.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(loaded.Steps))
	}
	if loaded.Steps[0].ID != "a" || loaded.Steps[1].ID != "b" || loaded.Steps[2].ID != "c" {
		t.Errorf("order = [%s, %s, %s], want [a, b, c]",
			loaded.Steps[0].ID, loaded.Steps[1].ID, loaded.Steps[2].ID)
	}
}

func TestResumeSuspended(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"after": "done"}}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	// Create a workflow that is already paused with an expired deadline
	wf := NewWorkflow("wf1", "Suspended", "test", []Step{
		{ID: "wait", Kind: StepSuspend, Config: map[string]any{
			"suspend_until_ms": float64(1000),
		}, State: StepCompleted},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "after"},
			DependsOn: []string{"wait"}, State: StepPending},
	})
	wf.State = StatePaused
	wf.Context["wait_suspend_until_ms"] = int64(1000) // long past
	_ = store.Save(wf)

	engine.resumeSuspended()

	// Give async resume a moment to complete
	for i := 0; i < 50; i++ {
		loaded, _ := store.Load("wf1")
		if loaded.State == StateCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed (watchdog should have resumed)", loaded.State)
	}
}
