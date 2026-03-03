package workflow

import (
	"context"
	"testing"
)

// --- unit tests for applyReducer ---

func TestApplyReducer_Replace(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"key": "old"}
	applyReducer(ctx, "key", "new", ReducerReplace)
	if ctx["key"] != "new" {
		t.Errorf("got %v, want new", ctx["key"])
	}
}

func TestApplyReducer_Append(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"items": []any{"a", "b"}}
	applyReducer(ctx, "items", "c", ReducerAppend)
	got, ok := ctx["items"].([]any)
	if !ok {
		t.Fatalf("items is not []any: %T", ctx["items"])
	}
	if len(got) != 3 || got[2] != "c" {
		t.Errorf("got %v, want [a b c]", got)
	}
}

func TestApplyReducer_AppendNewKey(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{}
	applyReducer(ctx, "items", "first", ReducerAppend)
	got, ok := ctx["items"].([]any)
	if !ok {
		t.Fatalf("items is not []any: %T", ctx["items"])
	}
	if len(got) != 1 || got[0] != "first" {
		t.Errorf("got %v, want [first]", got)
	}
}

func TestApplyReducer_Sum(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"total": float64(10)}
	applyReducer(ctx, "total", float64(5), ReducerSum)
	if ctx["total"] != float64(15) {
		t.Errorf("got %v, want 15", ctx["total"])
	}
}

func TestApplyReducer_SumInt64(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"count": int64(3)}
	applyReducer(ctx, "count", int64(7), ReducerSum)
	if ctx["count"] != int64(10) {
		t.Errorf("got %v, want 10", ctx["count"])
	}
}

func TestApplyReducer_SumNewKey(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{}
	applyReducer(ctx, "total", float64(42), ReducerSum)
	if ctx["total"] != float64(42) {
		t.Errorf("got %v, want 42", ctx["total"])
	}
}

func TestApplyReducer_Merge(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{
		"meta": map[string]any{"a": 1, "b": 2},
	}
	applyReducer(ctx, "meta", map[string]any{"b": 99, "c": 3}, ReducerMerge)
	got, ok := ctx["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta is not map[string]any: %T", ctx["meta"])
	}
	if got["a"] != 1 {
		t.Errorf("a = %v, want 1", got["a"])
	}
	if got["b"] != 99 {
		t.Errorf("b = %v, want 99", got["b"])
	}
	if got["c"] != 3 {
		t.Errorf("c = %v, want 3", got["c"])
	}
}

func TestApplyReducer_DefaultIsReplace(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"key": "old"}
	applyReducer(ctx, "key", "new", "")
	if ctx["key"] != "new" {
		t.Errorf("got %v, want new", ctx["key"])
	}
}

// --- integration test: engine with reducer configured ---

func TestEngine_ReducerAppend(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	// Single step writes context["a"]. With append reducer, the value
	// should be wrapped in a []any slice instead of being a plain string.
	wf := NewWorkflow("wf-red", "ReducerTest", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
	})
	wf.Reducers = map[string]ReducerKind{
		"a": ReducerAppend,
	}
	_ = store.Save(wf)

	if err := engine.Start(context.Background(), "wf-red"); err != nil {
		t.Fatal(err)
	}

	loaded, _ := store.Load("wf-red")
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s, want completed", loaded.State)
	}

	// With append reducer, context["a"] should be []any{"result from ta"}
	val, ok := loaded.Context["a"].([]any)
	if !ok {
		t.Fatalf("context[a] is not []any: %T (%v)", loaded.Context["a"], loaded.Context["a"])
	}
	if len(val) != 1 {
		t.Errorf("context[a] len = %d, want 1", len(val))
	}
	if val[0] != "result from ta" {
		t.Errorf("context[a][0] = %v, want 'result from ta'", val[0])
	}
}
