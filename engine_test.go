package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- engine: linear 3-step workflow ---

func TestLinearWorkflow(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{
		"step_a": "result_a",
		"step_b": "result_b",
		"step_c": "result_c",
	}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Linear", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "step_a"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "step_b"}, DependsOn: []string{"a"}, State: StepPending},
		{ID: "c", Kind: StepTool, Config: map[string]any{"tool": "step_c"}, DependsOn: []string{"b"}, State: StepPending},
	})
	_ = store.Save(wf)

	if err := engine.Start(context.Background(), "wf1"); err != nil {
		t.Fatal(err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}

	for _, s := range loaded.Steps {
		if s.State != StepCompleted {
			t.Errorf("step %s state = %s, want completed", s.ID, s.State)
		}
	}

	if loaded.Context["a"] != "result_a" {
		t.Errorf("context[a] = %v, want result_a", loaded.Context["a"])
	}
}

// --- engine: DAG with parallel deps ---

func TestDAGWorkflow(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "DAG", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "tb"}, State: StepPending},
		{ID: "c", Kind: StepTool, Config: map[string]any{"tool": "tc"}, DependsOn: []string{"a", "b"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	for _, s := range loaded.Steps {
		if s.State != StepCompleted {
			t.Errorf("step %s state = %s, want completed", s.ID, s.State)
		}
	}
}

// --- engine: condition step ---

func TestConditionStep(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{
		"check_weather": "temperature is 5 degrees, below freezing expected",
	}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Condition", "telegram:1", []Step{
		{ID: "check", Kind: StepTool, Config: map[string]any{"tool": "check_weather"}, State: StepPending},
		{ID: "eval", Kind: StepCondition, Config: map[string]any{
			"check":    "$steps.check",
			"contains": "below",
		}, DependsOn: []string{"check"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.Context["eval"] != "true" {
		t.Errorf("condition result = %v, want true", loaded.Context["eval"])
	}
}

func TestConditionStepFalse(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{
		"check_weather": "sunny and warm",
	}}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Condition", "telegram:1", []Step{
		{ID: "check", Kind: StepTool, Config: map[string]any{"tool": "check_weather"}, State: StepPending},
		{ID: "eval", Kind: StepCondition, Config: map[string]any{
			"check":    "$steps.check",
			"contains": "below",
		}, DependsOn: []string{"check"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.Context["eval"] != "false" {
		t.Errorf("condition result = %v, want false", loaded.Context["eval"])
	}
}

// --- engine: approval ---

func TestApprovalHalts(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Approval", "telegram:1", []Step{
		{ID: "do", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
		{ID: "approve", Kind: StepApproval, Config: map[string]any{"message": "Please approve"}, DependsOn: []string{"do"}, State: StepPending},
		{ID: "finish", Kind: StepTool, Config: map[string]any{"tool": "tb"}, DependsOn: []string{"approve"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval", loaded.State)
	}
	if loaded.GetStep("do").State != StepCompleted {
		t.Error("step 'do' should be completed")
	}
	if loaded.GetStep("approve").State != StepPending {
		t.Error("step 'approve' should be pending")
	}
	if loaded.GetStep("finish").State != StepPending {
		t.Error("step 'finish' should be pending")
	}
}

func TestApprovalResume(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Approval", "telegram:1", []Step{
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "finish", Kind: StepTool, Config: map[string]any{"tool": "ta"}, DependsOn: []string{"approve"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("state = %s, want waiting_approval", loaded.State)
	}

	if err := engine.HandleApproval("wf1", true); err != nil {
		t.Fatal(err)
	}
	_ = engine.RunToCompletion(context.Background(), "wf1")

	loaded, _ = store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

func TestApprovalReject(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Approval", "telegram:1", []Step{
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	_ = engine.HandleApproval("wf1", false)

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCancelled {
		t.Errorf("state = %s, want cancelled", loaded.State)
	}
}

// --- engine: error propagation ---

func TestErrorPropagation(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{err: errors.New("tool exploded")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Error", "telegram:1", []Step{
		{ID: "boom", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"boom"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateFailed {
		t.Errorf("state = %s, want failed", loaded.State)
	}
	if loaded.GetStep("boom").State != StepFailed {
		t.Error("step 'boom' should be failed")
	}
	if loaded.GetStep("after").State != StepPending {
		t.Error("step 'after' should still be pending")
	}
}

// --- engine: cancel ---

func TestCancel(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Cancel", "telegram:1", nil)
	wf.State = StateRunning
	_ = store.Save(wf)

	if err := engine.Cancel("wf1"); err != nil {
		t.Fatal(err)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCancelled {
		t.Errorf("state = %s, want cancelled", loaded.State)
	}
}

// --- context reference resolution ---

func TestResolveRef(t *testing.T) {
	t.Parallel()
	wf := &Workflow{Context: map[string]any{
		"check": "some result",
	}}

	got := resolveRef("$steps.check", wf)
	if got != "some result" {
		t.Errorf("resolveRef = %v, want 'some result'", got)
	}

	got = resolveRef("plain string", wf)
	if got != "plain string" {
		t.Errorf("resolveRef = %v, want 'plain string'", got)
	}

	got = resolveRef(42, wf)
	if got != 42 {
		t.Errorf("resolveRef = %v, want 42", got)
	}
}

func TestResolvePromptRefs(t *testing.T) {
	t.Parallel()
	wf := &Workflow{Context: map[string]any{
		"weather": "sunny",
		"temp":    25,
	}}

	got := resolvePromptRefs("The weather is {{weather}} and {{temp}} degrees", wf)
	want := "The weather is sunny and 25 degrees"
	if got != want {
		t.Errorf("resolvePromptRefs = %q, want %q", got, want)
	}
}

func TestParseOwner(t *testing.T) {
	t.Parallel()
	ch, id := ParseOwner("telegram:428660")
	if ch != "telegram" || id != "428660" {
		t.Errorf("ParseOwner = %q, %q", ch, id)
	}

	ch, id = ParseOwner("invalid")
	if ch != "" || id != "" {
		t.Errorf("ParseOwner invalid = %q, %q", ch, id)
	}
}

// --- parallel step execution ---

func TestParallelStepExecution(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Parallel", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "tb"}, State: StepPending},
		{ID: "c", Kind: StepTool, Config: map[string]any{"tool": "tc"}, DependsOn: []string{"a", "b"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	for _, s := range loaded.Steps {
		if s.State != StepCompleted {
			t.Errorf("step %s state = %s, want completed", s.ID, s.State)
		}
	}
}

// --- basic retry ---

func TestRetryOnFailure(t *testing.T) {
	t.Parallel()
	callCount := 0
	runner := &countingToolRunner{
		callCount: &callCount,
		failUntil: 2,
	}
	engine, store := newTestEngine(t, runner)
	engine.executors[StepTool] = NewToolExecutor(runner)

	wf := NewWorkflow("wf1", "Retry", "telegram:1", []Step{
		{ID: "flaky", Kind: StepTool, Config: map[string]any{
			"tool": "unstable",
			"retry": map[string]any{
				"max":      float64(3),
				"delay_ms": float64(1),
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed (retries should have fixed it)", loaded.State)
	}
	if callCount != 3 {
		t.Errorf("call count = %d, want 3 (2 failures + 1 success)", callCount)
	}
}

func TestRetryExhausted(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{err: errors.New("always fails")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "RetryExhausted", "telegram:1", []Step{
		{ID: "doom", Kind: StepTool, Config: map[string]any{
			"tool": "bad",
			"retry": map[string]any{
				"max":      float64(2),
				"delay_ms": float64(1),
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateFailed {
		t.Errorf("state = %s, want failed", loaded.State)
	}
	step := loaded.GetStep("doom")
	if step.Retries != 2 {
		t.Errorf("retries = %d, want 2", step.Retries)
	}
}

// --- on_error=skip ---

func TestOnErrorSkip(t *testing.T) {
	t.Parallel()
	engine, store := newTestEngine(t, &mockToolRunner{})
	engine.executors[StepTool] = NewToolExecutor(&selectiveToolRunner{
		failTools: map[string]bool{"bad": true},
	})

	wf := NewWorkflow("wf1", "Skip", "telegram:1", []Step{
		{ID: "fragile", Kind: StepTool, Config: map[string]any{
			"tool":     "bad",
			"on_error": OnErrorSkip,
		}, State: StepPending},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"fragile"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed (fragile should be skipped)", loaded.State)
	}
	if loaded.GetStep("fragile").State != StepSkipped {
		t.Errorf("fragile state = %s, want skipped", loaded.GetStep("fragile").State)
	}
	if loaded.GetStep("after").State != StepCompleted {
		t.Errorf("after state = %s, want completed", loaded.GetStep("after").State)
	}
}

// --- sub-workflows ---

func TestSubWorkflow(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{results: map[string]string{
		"child_tool": "child_result",
		"parent_end": "done",
	}}
	engine, store := newTestEngine(t, runner)
	engine.executors[StepWorkflow] = NewSubWorkflowExecutor(engine)

	child := NewWorkflow("child1", "Child", "telegram:1", []Step{
		{ID: "c1", Kind: StepTool, Config: map[string]any{"tool": "child_tool"}, State: StepPending},
	})
	_ = store.Save(child)

	parent := NewWorkflow("parent1", "Parent", "telegram:1", []Step{
		{ID: "sub", Kind: StepWorkflow, Config: map[string]any{"workflow_id": "child1"}, State: StepPending},
		{ID: "done", Kind: StepTool, Config: map[string]any{"tool": "parent_end"}, DependsOn: []string{"sub"}, State: StepPending},
	})
	_ = store.Save(parent)
	_ = engine.Start(context.Background(), "parent1")

	loaded, _ := store.Load("parent1")
	if loaded.State != StateCompleted {
		t.Errorf("parent state = %s, want completed", loaded.State)
	}

	childLoaded, _ := store.Load("child1")
	if childLoaded.State != StateCompleted {
		t.Errorf("child state = %s, want completed", childLoaded.State)
	}

	subCtx, ok := loaded.Context["sub"].(map[string]any)
	if !ok {
		t.Fatalf("parent context[sub] type = %T, want map", loaded.Context["sub"])
	}
	if subCtx["c1"] != "child_result" {
		t.Errorf("parent context[sub][c1] = %v, want child_result", subCtx["c1"])
	}
}

func TestSubWorkflowChildFailure(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{err: errors.New("child fails")}
	engine, store := newTestEngine(t, runner)
	engine.executors[StepWorkflow] = NewSubWorkflowExecutor(engine)

	child := NewWorkflow("child1", "Child", "telegram:1", []Step{
		{ID: "c1", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
	})
	_ = store.Save(child)

	parent := NewWorkflow("parent1", "Parent", "telegram:1", []Step{
		{ID: "sub", Kind: StepWorkflow, Config: map[string]any{"workflow_id": "child1"}, State: StepPending},
	})
	_ = store.Save(parent)
	_ = engine.Start(context.Background(), "parent1")

	loaded, _ := store.Load("parent1")
	if loaded.State != StateFailed {
		t.Errorf("parent state = %s, want failed (child failed)", loaded.State)
	}
}

// --- templates ---

func TestTemplateInstantiate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tmpl := `{
		"name": "Monitor {{service}}",
		"description": "Check {{service}} health every {{interval}}",
		"params": {"service": "service name", "interval": "check interval"},
		"defaults": {"interval": "1m"},
		"steps": [
			{"id": "check", "kind": "tool", "config": {"tool": "check_health", "args": {"service": "{{service}}"}}},
			{"id": "notify", "kind": "message", "config": {"content": "{{service}} status: {{check}}"}, "depends_on": ["check"]}
		]
	}`
	_ = os.WriteFile(filepath.Join(dir, "monitor.json"), []byte(tmpl), 0600)

	ts := NewTemplateStore(dir)
	names := ts.List()
	if len(names) != 1 || names[0] != "monitor" {
		t.Fatalf("template list = %v, want [monitor]", names)
	}

	wf, err := ts.Instantiate("monitor", "wf1", "telegram:1", map[string]any{
		"service": "api-gateway",
	})
	if err != nil {
		t.Fatal(err)
	}

	if wf.Name != "Monitor api-gateway" {
		t.Errorf("name = %q, want %q", wf.Name, "Monitor api-gateway")
	}
	if wf.Description != "Check api-gateway health every 1m" {
		t.Errorf("description = %q", wf.Description)
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(wf.Steps))
	}

	args, ok := wf.Steps[0].Config["args"].(map[string]any)
	if !ok {
		t.Fatalf("step[0] args type = %T", wf.Steps[0].Config["args"])
	}
	if args["service"] != "api-gateway" {
		t.Errorf("step[0] args.service = %v, want api-gateway", args["service"])
	}
	if len(wf.Steps[1].DependsOn) != 1 || wf.Steps[1].DependsOn[0] != "check" {
		t.Errorf("step[1] depends_on = %v, want [check]", wf.Steps[1].DependsOn)
	}
}

func TestTemplateNotFound(t *testing.T) {
	t.Parallel()
	ts := NewTemplateStore(t.TempDir())
	_, err := ts.Instantiate("nonexistent", "wf1", "", nil)
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestTemplateReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := NewTemplateStore(dir)

	if len(ts.List()) != 0 {
		t.Fatal("expected empty template list")
	}

	tmpl := `{"name": "Test", "steps": [{"id": "s1", "kind": "tool", "config": {"tool": "t"}}]}`
	_ = os.WriteFile(filepath.Join(dir, "test.json"), []byte(tmpl), 0600)

	ts.Reload()
	if len(ts.List()) != 1 {
		t.Errorf("after reload: templates = %d, want 1", len(ts.List()))
	}
}

// --- AllowedTools permission scope ---

func TestAllowedToolsPermitted(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Scoped", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "safe_tool"}, State: StepPending},
	})
	wf.AllowedTools = []string{"safe_tool", "another_tool"}
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

func TestAllowedToolsBlocked(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Scoped", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "dangerous_tool"}, State: StepPending},
	})
	wf.AllowedTools = []string{"safe_tool"}
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateFailed {
		t.Errorf("state = %s, want failed (tool not allowed)", loaded.State)
	}
	if loaded.GetStep("a").State != StepFailed {
		t.Errorf("step state = %s, want failed", loaded.GetStep("a").State)
	}
}

func TestAllowedToolsEmpty(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Open", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "any_tool"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

// --- Metrics ---

func TestMetrics(t *testing.T) {
	t.Parallel()

	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)
	m := engine.metrics

	wf := NewWorkflow("wf1", "Metrics", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	if m.WorkflowsCreated.Load() != 1 {
		t.Errorf("created = %d, want 1", m.WorkflowsCreated.Load())
	}
	if m.WorkflowsCompleted.Load() != 1 {
		t.Errorf("completed = %d, want 1", m.WorkflowsCompleted.Load())
	}
	if m.StepsExecuted.Load() != 1 {
		t.Errorf("steps executed = %d, want 1", m.StepsExecuted.Load())
	}

	summary := m.Summary()
	if summary == "" {
		t.Error("summary should not be empty")
	}
}

func TestMetricsCancel(t *testing.T) {
	t.Parallel()

	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)
	m := engine.metrics

	wf := NewWorkflow("wf1", "Cancel", "telegram:1", nil)
	wf.State = StateRunning
	_ = store.Save(wf)
	_ = engine.Cancel("wf1")

	if m.WorkflowsCancelled.Load() != 1 {
		t.Errorf("cancelled = %d, want 1", m.WorkflowsCancelled.Load())
	}
}

func TestHandleApprovalWithData(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, s := newTestEngine(t, runner)

	wf := NewWorkflow("wf-data", "Test", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepCompleted},
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}, DependsOn: []string{"s1"}, State: StepPending},
	})
	_ = s.Save(wf)
	_ = s.Modify("wf-data", func(w *Workflow) {
		w.State = StateWaitingApproval
	})

	data := map[string]any{"selected": []any{"place1", "place2"}}
	if err := engine.HandleApprovalWithData("wf-data", true, data); err != nil {
		t.Fatalf("HandleApprovalWithData: %v", err)
	}

	loaded, _ := s.Load("wf-data")
	if loaded.State != StateRunning {
		t.Errorf("expected running, got %s", loaded.State)
	}
	ctx, ok := loaded.Context["approve"].(map[string]any)
	if !ok {
		t.Fatalf("expected map in context, got %T", loaded.Context["approve"])
	}
	if ctx["selected"] == nil {
		t.Error("missing selected in context data")
	}
}

func TestHandleApprovalWithData_NilFallback(t *testing.T) {
	t.Parallel()
	runner := &mockToolRunner{}
	engine, s := newTestEngine(t, runner)

	wf := NewWorkflow("wf-nil", "Test", "test", []Step{
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
	})
	_ = s.Save(wf)
	_ = s.Modify("wf-nil", func(w *Workflow) { w.State = StateWaitingApproval })

	if err := engine.HandleApprovalWithData("wf-nil", true, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	loaded, _ := s.Load("wf-nil")
	if loaded.Context["approve"] != "approved" {
		t.Errorf("nil data should fall back to 'approved', got %v", loaded.Context["approve"])
	}
}

// suppress unused import warnings
var (
	_ = fmt.Sprintf
	_ = strings.Contains
)
