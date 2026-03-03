package workflow

import (
	"context"
	"testing"
	"time"
)

func TestForEachExecutor_WaitChildren_TDD(t *testing.T) {
	runner := &slowToolRunner{delay: 50 * time.Millisecond}
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(runner))

	wf := NewWorkflow("wf_red", "RedTest", "test", []Step{
		{ID: "produce", Kind: StepTool, Config: map[string]any{"tool": "produce"}, State: StepPending},
		// ForEach injects children. Parent must NOT be "completed" until all children are!
		{ID: "loop", Kind: StepForEach, Config: map[string]any{
			"items":       "produce",
			"step_kind":   "tool",
			"step_config": map[string]any{"tool": "slow_item"},
		}, DependsOn: []string{"produce"}, State: StepPending},
		// Next step depends on 'loop'. It should wait for ALL loop children.
		{ID: "after_loop", Kind: StepTool, Config: map[string]any{"tool": "after"},
			DependsOn: []string{"loop"}, State: StepPending},
	})
	wf.Context["produce"] = []any{"a", "b"}
	_ = store.Save(wf)

	_ = store.Modify("wf_red", func(w *Workflow) {
		w.GetStep("produce").State = StepCompleted
		w.GetStep("produce").Result = "ok"
	})

	err := engine.Start(context.Background(), "wf_red")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	loaded, _ := store.Load("wf_red")
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s, want completed", loaded.State)
	}

	loop0 := loaded.GetStep("loop_0")
	loop1 := loaded.GetStep("loop_1")
	after := loaded.GetStep("after_loop")

	if loop0 == nil || loop1 == nil || after == nil {
		t.Fatal("missing steps")
	}

	// The 'after' step MUST have executed AFTER 'loop_0' and 'loop_1' finished.
	// We can verify this by checking the StartedAt timestamp of 'after' vs EndedAt of children.
	if after.StartedAt < loop0.EndedAt {
		t.Errorf("FAIL: 'after_loop' started at %d, but 'loop_0' ended at %d! Downstream step ran before injected children finished!", after.StartedAt, loop0.EndedAt)
	}
	if after.StartedAt < loop1.EndedAt {
		t.Errorf("FAIL: 'after_loop' started at %d, but 'loop_1' ended at %d! Downstream step ran before injected children finished!", after.StartedAt, loop1.EndedAt)
	}
}
