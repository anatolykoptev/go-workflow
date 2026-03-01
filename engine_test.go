package workflow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- mock tool runner ---

type mockToolRunner struct {
	results map[string]string
	err     error
}

func (m *mockToolRunner) Execute(_ context.Context, name string, args map[string]any) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return "result from " + name, nil
}

// --- helpers ---

func newTestStore(t *testing.T) *WorkflowStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewWorkflowStore(filepath.Join(dir, "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newTestEngine(t *testing.T, runner ToolRunner) (*Engine, *WorkflowStore) {
	t.Helper()
	store := newTestStore(t)
	// Use nil for provider and bus since we test tool/condition/approval executors directly.
	// LLM and message executors need real interfaces but we test those separately.
	executors := map[StepKind]StepExecutor{
		StepTool:      NewToolExecutor(runner),
		StepCondition: NewConditionExecutor(),
		StepApproval:  NewApprovalExecutor(),
	}
	engine := &Engine{store: store, executors: executors}
	return engine, store
}

// --- store tests ---

func TestStoreSaveLoad(t *testing.T) {
	store := newTestStore(t)

	wf := NewWorkflow("wf1", "Test", "telegram:123", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "read_file"}, State: StepPending},
	})

	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	loaded, ok := store.Load("wf1")
	if !ok {
		t.Fatal("workflow not found after save")
	}
	if loaded.Name != "Test" {
		t.Errorf("name = %q, want %q", loaded.Name, "Test")
	}
	if loaded.Owner != "telegram:123" {
		t.Errorf("owner = %q, want %q", loaded.Owner, "telegram:123")
	}
}

func TestStoreDelete(t *testing.T) {
	store := newTestStore(t)
	wf := NewWorkflow("wf1", "Test", "", nil)
	_ = store.Save(wf)

	if err := store.Delete("wf1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Load("wf1"); ok {
		t.Error("workflow still present after delete")
	}
}

func TestStoreList(t *testing.T) {
	store := newTestStore(t)
	_ = store.Save(NewWorkflow("wf1", "A", "", nil))

	wf2 := NewWorkflow("wf2", "B", "", nil)
	wf2.State = StateRunning
	_ = store.Save(wf2)

	all := store.List("")
	if len(all) != 2 {
		t.Errorf("list all = %d, want 2", len(all))
	}

	running := store.List(StateRunning)
	if len(running) != 1 {
		t.Errorf("list running = %d, want 1", len(running))
	}
}

func TestStoreListByOwner(t *testing.T) {
	store := newTestStore(t)
	_ = store.Save(NewWorkflow("wf1", "A", "telegram:1", nil))
	_ = store.Save(NewWorkflow("wf2", "B", "telegram:2", nil))
	_ = store.Save(NewWorkflow("wf3", "C", "telegram:1", nil))

	owned := store.ListByOwner("telegram:1")
	if len(owned) != 2 {
		t.Errorf("owned = %d, want 2", len(owned))
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")

	// Create and save
	store1, _ := NewWorkflowStore(wfDir)
	wf := NewWorkflow("wf1", "Persistent", "", []Step{
		{ID: "s1", Kind: StepTool, State: StepCompleted},
	})
	_ = store1.Save(wf)

	// Reload from disk
	store2, _ := NewWorkflowStore(wfDir)
	loaded, ok := store2.Load("wf1")
	if !ok {
		t.Fatal("workflow not found after reload")
	}
	if loaded.Name != "Persistent" {
		t.Errorf("name = %q, want %q", loaded.Name, "Persistent")
	}
	if len(loaded.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(loaded.Steps))
	}
}

// --- types tests ---

func TestWorkflowGetStep(t *testing.T) {
	wf := NewWorkflow("wf1", "Test", "", []Step{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	})

	if s := wf.GetStep("b"); s == nil || s.ID != "b" {
		t.Error("GetStep(b) failed")
	}
	if s := wf.GetStep("z"); s != nil {
		t.Error("GetStep(z) should return nil")
	}
}

func TestWorkflowIsTerminal(t *testing.T) {
	wf := NewWorkflow("wf1", "Test", "", nil)

	wf.State = StatePending
	if wf.IsTerminal() {
		t.Error("pending should not be terminal")
	}

	wf.State = StateCompleted
	if !wf.IsTerminal() {
		t.Error("completed should be terminal")
	}

	wf.State = StateFailed
	if !wf.IsTerminal() {
		t.Error("failed should be terminal")
	}
}

// --- engine: linear 3-step workflow ---

func TestLinearWorkflow(t *testing.T) {
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

	// Verify context was populated
	if loaded.Context["a"] != "result_a" {
		t.Errorf("context[a] = %v, want result_a", loaded.Context["a"])
	}
}

// --- engine: DAG with parallel deps ---

func TestDAGWorkflow(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	// A and B have no deps (independent), C depends on both
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

	// All three should be completed
	for _, s := range loaded.Steps {
		if s.State != StepCompleted {
			t.Errorf("step %s state = %s, want completed", s.ID, s.State)
		}
	}
}

// --- engine: condition step ---

func TestConditionStep(t *testing.T) {
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

// --- engine: approval step halts workflow ---

func TestApprovalHalts(t *testing.T) {
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

	// Step "do" should be completed, "approve" pending, "finish" pending
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

// --- engine: approve → resume ---

func TestApprovalResume(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Approval", "telegram:1", []Step{
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}, State: StepPending},
		{ID: "finish", Kind: StepTool, Config: map[string]any{"tool": "ta"}, DependsOn: []string{"approve"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	// Should be waiting
	loaded, _ := store.Load("wf1")
	if loaded.State != StateWaitingApproval {
		t.Fatalf("state = %s, want waiting_approval", loaded.State)
	}

	// Approve
	if err := engine.HandleApproval("wf1", true); err != nil {
		t.Fatal(err)
	}

	// Resume
	_ = engine.RunToCompletion(context.Background(), "wf1")

	loaded, _ = store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

// --- engine: approval rejection ---

func TestApprovalReject(t *testing.T) {
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

// --- store: atomic write ---

func TestStoreAtomicWrite(t *testing.T) {
	store := newTestStore(t)

	wf := NewWorkflow("wf1", "Atomic", "", nil)
	_ = store.Save(wf)

	// Verify .json file exists, .tmp does not
	jsonPath := filepath.Join(store.dir, "wf1.json")
	tmpPath := jsonPath + ".tmp"

	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("json file not found: %v", err)
	}
	if _, err := os.Stat(tmpPath); !errors.Is(err, fs.ErrNotExist) {
		t.Error("tmp file should not exist after save")
	}
}

// --- context reference resolution ---

func TestResolveRef(t *testing.T) {
	wf := &Workflow{Context: map[string]any{
		"check": "some result",
	}}

	// $steps.check → "some result"
	got := resolveRef("$steps.check", wf)
	if got != "some result" {
		t.Errorf("resolveRef = %v, want 'some result'", got)
	}

	// Non-reference passes through
	got = resolveRef("plain string", wf)
	if got != "plain string" {
		t.Errorf("resolveRef = %v, want 'plain string'", got)
	}

	// Non-string passes through
	got = resolveRef(42, wf)
	if got != 42 {
		t.Errorf("resolveRef = %v, want 42", got)
	}
}

func TestResolvePromptRefs(t *testing.T) {
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
	ch, id := ParseOwner("telegram:428660")
	if ch != "telegram" || id != "428660" {
		t.Errorf("ParseOwner = %q, %q", ch, id)
	}

	ch, id = ParseOwner("invalid")
	if ch != "" || id != "" {
		t.Errorf("ParseOwner invalid = %q, %q", ch, id)
	}
}

// --- Phase 5.1: parallel step execution ---

func TestParallelStepExecution(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	// A and B are independent (no deps), C depends on both
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
	// A and B ran (possibly in parallel), then C after both
	for _, s := range loaded.Steps {
		if s.State != StepCompleted {
			t.Errorf("step %s state = %s, want completed", s.ID, s.State)
		}
	}
}

// --- Phase 5.2: retry ---

func TestRetryOnFailure(t *testing.T) {
	callCount := 0
	runner := &countingToolRunner{
		callCount: &callCount,
		failUntil: 2, // fail first 2 calls, succeed on 3rd
	}
	engine, store := newTestEngine(t, runner)
	// Register tool executor with the counting runner
	engine.executors[StepTool] = NewToolExecutor(runner)

	wf := NewWorkflow("wf1", "Retry", "telegram:1", []Step{
		{ID: "flaky", Kind: StepTool, Config: map[string]any{
			"tool": "unstable",
			"retry": map[string]any{
				"max":      float64(3), // JSON numbers are float64
				"delay_ms": float64(1), // minimal delay for tests
			},
		}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed (retries should have fixed it)", loaded.State)
	}
	if loaded.GetStep("flaky").State != StepCompleted {
		t.Errorf("step state = %s, want completed", loaded.GetStep("flaky").State)
	}
	if callCount != 3 {
		t.Errorf("call count = %d, want 3 (2 failures + 1 success)", callCount)
	}
}

func TestRetryExhausted(t *testing.T) {
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

// --- Phase 5.2: on_error=skip ---

func TestOnErrorSkip(t *testing.T) {
	runner := &mockToolRunner{err: errors.New("fails")}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Skip", "telegram:1", []Step{
		{ID: "fragile", Kind: StepTool, Config: map[string]any{
			"tool":     "bad",
			"on_error": OnErrorSkip,
		}, State: StepPending},
		{ID: "after", Kind: StepTool, Config: map[string]any{"tool": "good"}, DependsOn: []string{"fragile"}, State: StepPending},
	})
	_ = store.Save(wf)

	// Override runner for "after" step — need a runner that succeeds for "good"
	engine.executors[StepTool] = NewToolExecutor(&selectiveToolRunner{
		failTools: map[string]bool{"bad": true},
	})

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

// --- Phase 5.3: sub-workflows ---

func TestSubWorkflow(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{
		"child_tool": "child_result",
		"parent_end": "done",
	}}
	engine, store := newTestEngine(t, runner)
	// Register sub-workflow executor
	engine.executors[StepWorkflow] = NewSubWorkflowExecutor(engine)

	// Create child workflow first
	child := NewWorkflow("child1", "Child", "telegram:1", []Step{
		{ID: "c1", Kind: StepTool, Config: map[string]any{"tool": "child_tool"}, State: StepPending},
	})
	_ = store.Save(child)

	// Parent workflow references child
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

	// Child should also be completed
	childLoaded, _ := store.Load("child1")
	if childLoaded.State != StateCompleted {
		t.Errorf("child state = %s, want completed", childLoaded.State)
	}

	// Parent context should have child's context
	subCtx, ok := loaded.Context["sub"].(map[string]any)
	if !ok {
		t.Fatalf("parent context[sub] type = %T, want map", loaded.Context["sub"])
	}
	if subCtx["c1"] != "child_result" {
		t.Errorf("parent context[sub][c1] = %v, want child_result", subCtx["c1"])
	}
}

func TestSubWorkflowChildFailure(t *testing.T) {
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

// --- Phase 5.4: templates ---

func TestTemplateInstantiate(t *testing.T) {
	dir := t.TempDir()

	// Write a template file
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
	_ = os.WriteFile(filepath.Join(dir, "monitor.json"), []byte(tmpl), 0644)

	ts := NewTemplateStore(dir)

	// List templates
	names := ts.List()
	if len(names) != 1 || names[0] != "monitor" {
		t.Fatalf("template list = %v, want [monitor]", names)
	}

	// Instantiate with params
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
		t.Errorf("description = %q, want %q", wf.Description, "Check api-gateway health every 1m")
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(wf.Steps))
	}
	if wf.Steps[0].Config["tool"] != "check_health" {
		t.Errorf("step[0] tool = %v", wf.Steps[0].Config["tool"])
	}

	// Check that args were substituted
	args, ok := wf.Steps[0].Config["args"].(map[string]any)
	if !ok {
		t.Fatalf("step[0] args type = %T", wf.Steps[0].Config["args"])
	}
	if args["service"] != "api-gateway" {
		t.Errorf("step[0] args.service = %v, want api-gateway", args["service"])
	}

	// Check deps preserved
	if len(wf.Steps[1].DependsOn) != 1 || wf.Steps[1].DependsOn[0] != "check" {
		t.Errorf("step[1] depends_on = %v, want [check]", wf.Steps[1].DependsOn)
	}
}

func TestTemplateNotFound(t *testing.T) {
	ts := NewTemplateStore(t.TempDir())
	_, err := ts.Instantiate("nonexistent", "wf1", "", nil)
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestTemplateReload(t *testing.T) {
	dir := t.TempDir()
	ts := NewTemplateStore(dir)

	if len(ts.List()) != 0 {
		t.Fatal("expected empty template list")
	}

	// Add a template file
	tmpl := `{"name": "Test", "steps": [{"id": "s1", "kind": "tool", "config": {"tool": "t"}}]}`
	_ = os.WriteFile(filepath.Join(dir, "test.json"), []byte(tmpl), 0644)

	// Reload
	ts.Reload()
	if len(ts.List()) != 1 {
		t.Errorf("after reload: templates = %d, want 1", len(ts.List()))
	}
}

// --- additional mock runners ---

type countingToolRunner struct {
	callCount *int
	failUntil int // fail the first N calls
}

func (r *countingToolRunner) Execute(_ context.Context, name string, args map[string]any) (string, error) {
	*r.callCount++
	if *r.callCount <= r.failUntil {
		return "", fmt.Errorf("attempt %d failed", *r.callCount)
	}
	return "result from " + name, nil
}

type selectiveToolRunner struct {
	failTools map[string]bool
}

func (r *selectiveToolRunner) Execute(_ context.Context, name string, args map[string]any) (string, error) {
	if r.failTools[name] {
		return "", fmt.Errorf("tool %s failed", name)
	}
	return "result from " + name, nil
}

// --- Phase 6.3: AllowedTools permission scope ---

func TestAllowedToolsPermitted(t *testing.T) {
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
	// Empty AllowedTools = all tools permitted (backwards compat)
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Open", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "any_tool"}, State: StepPending},
	})
	// No AllowedTools set
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

// --- Phase 6.4: PauseAll / ResumeAll ---

func TestPauseAll(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	// Create workflows in different states
	wf1 := NewWorkflow("wf1", "Running", "telegram:1", nil)
	wf1.State = StateRunning
	_ = store.Save(wf1)

	wf2 := NewWorkflow("wf2", "Waiting", "telegram:1", nil)
	wf2.State = StateWaitingApproval
	_ = store.Save(wf2)

	wf3 := NewWorkflow("wf3", "Completed", "telegram:1", nil)
	wf3.State = StateCompleted
	_ = store.Save(wf3)

	n := engine.PauseAll()
	if n != 2 {
		t.Errorf("paused = %d, want 2 (running + waiting_approval)", n)
	}

	loaded1, _ := store.Load("wf1")
	if loaded1.State != StatePaused {
		t.Errorf("wf1 state = %s, want paused", loaded1.State)
	}

	loaded2, _ := store.Load("wf2")
	if loaded2.State != StatePaused {
		t.Errorf("wf2 state = %s, want paused", loaded2.State)
	}

	// Completed should not be paused
	loaded3, _ := store.Load("wf3")
	if loaded3.State != StateCompleted {
		t.Errorf("wf3 state = %s, want completed (unchanged)", loaded3.State)
	}
}

// --- Phase 6.5: Metrics ---

func TestMetrics(t *testing.T) {
	GlobalMetrics.Reset()

	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	// Run a workflow to completion
	wf := NewWorkflow("wf1", "Metrics", "telegram:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	if GlobalMetrics.WorkflowsCreated.Load() != 1 {
		t.Errorf("created = %d, want 1", GlobalMetrics.WorkflowsCreated.Load())
	}
	if GlobalMetrics.WorkflowsCompleted.Load() != 1 {
		t.Errorf("completed = %d, want 1", GlobalMetrics.WorkflowsCompleted.Load())
	}
	if GlobalMetrics.StepsExecuted.Load() != 1 {
		t.Errorf("steps executed = %d, want 1", GlobalMetrics.StepsExecuted.Load())
	}

	// Summary should contain data
	summary := GlobalMetrics.Summary()
	if summary == "" {
		t.Error("summary should not be empty")
	}
}

func TestMetricsCancel(t *testing.T) {
	GlobalMetrics.Reset()

	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	wf := NewWorkflow("wf1", "Cancel", "telegram:1", nil)
	wf.State = StateRunning
	_ = store.Save(wf)
	_ = engine.Cancel("wf1")

	if GlobalMetrics.WorkflowsCancelled.Load() != 1 {
		t.Errorf("cancelled = %d, want 1", GlobalMetrics.WorkflowsCancelled.Load())
	}
}

// --- AgentExecutor tests ---

type mockAgentRunner struct {
	result string
	err    error
}

func (m *mockAgentRunner) RunTask(_ context.Context, task string, sessionKey string, opts AgentRunOpts) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.result, nil
}

func TestAgentExecutor_Success(t *testing.T) {
	GlobalMetrics.Reset()

	runner := &mockAgentRunner{result: "agent output"}
	executor := NewAgentExecutor(runner)

	wf := NewWorkflow("wf1", "Agent", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepAgent, Config: map[string]any{
		"task": "Analyze this data",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step.Result != "agent output" {
		t.Errorf("result = %q, want %q", step.Result, "agent output")
	}
	if wf.Context["s1"] != "agent output" {
		t.Errorf("context[s1] = %v, want %q", wf.Context["s1"], "agent output")
	}
	if GlobalMetrics.AgentStepsExecuted.Load() != 1 {
		t.Errorf("AgentStepsExecuted = %d, want 1", GlobalMetrics.AgentStepsExecuted.Load())
	}
}

func TestAgentExecutor_MissingTask(t *testing.T) {
	runner := &mockAgentRunner{result: "ok"}
	executor := NewAgentExecutor(runner)

	wf := NewWorkflow("wf1", "Agent", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepAgent, Config: map[string]any{}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestAgentExecutor_Failure(t *testing.T) {
	GlobalMetrics.Reset()

	runner := &mockAgentRunner{err: errors.New("agent crashed")}
	executor := NewAgentExecutor(runner)

	wf := NewWorkflow("wf1", "Agent", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepAgent, Config: map[string]any{
		"task": "do something",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error")
	}
	if GlobalMetrics.AgentStepsFailed.Load() != 1 {
		t.Errorf("AgentStepsFailed = %d, want 1", GlobalMetrics.AgentStepsFailed.Load())
	}
}

func TestAgentExecutor_ContextRefs(t *testing.T) {
	var receivedTask string
	runner := &mockAgentRunner{result: "ok"}
	executor := &AgentExecutor{runner: &taskCapturingRunner{
		inner:    runner,
		captured: &receivedTask,
	}}

	wf := NewWorkflow("wf1", "Agent", "telegram:1", nil)
	wf.Context["fetch"] = "raw data here"
	step := &Step{ID: "s1", Kind: StepAgent, Config: map[string]any{
		"task": "Analyze: {{fetch}}",
	}}

	_ = executor.Execute(context.Background(), step, wf)
	if receivedTask != "Analyze: raw data here" {
		t.Errorf("task = %q, want resolved refs", receivedTask)
	}
}

type taskCapturingRunner struct {
	inner    AgentRunner
	captured *string
}

func (r *taskCapturingRunner) RunTask(ctx context.Context, task string, sessionKey string, opts AgentRunOpts) (string, error) {
	*r.captured = task
	return r.inner.RunTask(ctx, task, sessionKey, opts)
}

func TestWorkflowWithAgentStep(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"read_file": "file contents"}}
	engine, store := newTestEngine(t, runner)

	// Register agent executor
	engine.SetAgentRunner(&mockAgentRunner{result: "analyzed"})

	wf := NewWorkflow("wf1", "AgentFlow", "telegram:1", []Step{
		{ID: "read", Kind: StepTool, Config: map[string]any{"tool": "read_file"}, State: StepPending},
		{ID: "analyze", Kind: StepAgent, Config: map[string]any{
			"task": "Analyze: {{read}}",
		}, DependsOn: []string{"read"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	if loaded.GetStep("analyze").State != StepCompleted {
		t.Errorf("analyze state = %s, want completed", loaded.GetStep("analyze").State)
	}
	if loaded.Context["analyze"] != "analyzed" {
		t.Errorf("context[analyze] = %v, want %q", loaded.Context["analyze"], "analyzed")
	}
}

// --- A2A step tests ---

type mockA2ACaller struct {
	result string
	err    error
}

func (m *mockA2ACaller) Call(_ context.Context, agentID, message string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.result, nil
}

func TestEngine_A2AStep(t *testing.T) {
	GlobalMetrics.Reset()

	runner := &mockToolRunner{results: map[string]string{"read_file": "file contents"}}
	engine, store := newTestEngine(t, runner)
	engine.SetA2ACaller(&mockA2ACaller{result: "review complete"})

	wf := NewWorkflow("wf1", "A2AFlow", "telegram:1", []Step{
		{ID: "read", Kind: StepTool, Config: map[string]any{"tool": "read_file"}, State: StepPending},
		{ID: "review", Kind: StepA2A, Config: map[string]any{
			"agent_id": "code-review",
			"message":  "Review: {{read}}",
		}, DependsOn: []string{"read"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	if loaded.GetStep("review").State != StepCompleted {
		t.Errorf("review state = %s, want completed", loaded.GetStep("review").State)
	}
	if loaded.Context["review"] != "review complete" {
		t.Errorf("context[review] = %v, want %q", loaded.Context["review"], "review complete")
	}
	if GlobalMetrics.A2AStepsExecuted.Load() != 1 {
		t.Errorf("A2AStepsExecuted = %d, want 1", GlobalMetrics.A2AStepsExecuted.Load())
	}
}

func TestA2AExecutor_MissingConfig(t *testing.T) {
	caller := &mockA2ACaller{result: "ok"}
	executor := NewA2AExecutor(caller)

	wf := NewWorkflow("wf1", "A2A", "telegram:1", nil)

	// Missing agent_id
	step := &Step{ID: "s1", Kind: StepA2A, Config: map[string]any{"message": "hello"}}
	err := executor.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "agent_id") {
		t.Errorf("expected agent_id error, got: %v", err)
	}

	// Missing message
	step = &Step{ID: "s2", Kind: StepA2A, Config: map[string]any{"agent_id": "test"}}
	err = executor.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "message") {
		t.Errorf("expected message error, got: %v", err)
	}
}

func TestA2AExecutor_Failure(t *testing.T) {
	GlobalMetrics.Reset()

	caller := &mockA2ACaller{err: errors.New("connection refused")}
	executor := NewA2AExecutor(caller)

	wf := NewWorkflow("wf1", "A2A", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepA2A, Config: map[string]any{
		"agent_id": "broken",
		"message":  "hello",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error")
	}
	if GlobalMetrics.A2AStepsFailed.Load() != 1 {
		t.Errorf("A2AStepsFailed = %d, want 1", GlobalMetrics.A2AStepsFailed.Load())
	}
}

func TestA2AExecutor_ContextRefs(t *testing.T) {
	var receivedMsg string
	caller := &mockA2ACaller{result: "ok"}
	executor := NewA2AExecutor(&msgCapturingCaller{inner: caller, captured: &receivedMsg})

	wf := NewWorkflow("wf1", "A2A", "telegram:1", nil)
	wf.Context["diff"] = "some diff output"
	step := &Step{ID: "s1", Kind: StepA2A, Config: map[string]any{
		"agent_id": "reviewer",
		"message":  "Review this: {{diff}}",
	}}

	_ = executor.Execute(context.Background(), step, wf)
	if receivedMsg != "Review this: some diff output" {
		t.Errorf("message = %q, want resolved refs", receivedMsg)
	}
}

type msgCapturingCaller struct {
	inner    A2ACaller
	captured *string
}

func (c *msgCapturingCaller) Call(ctx context.Context, agentID, message string) (string, error) {
	*c.captured = message
	return c.inner.Call(ctx, agentID, message)
}

// --- Workflow Hooks tests ---

type hookRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *hookRecorder) Fire(event string, data map[string]any) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return 1
}

func (r *hookRecorder) getEvents() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

func TestWorkflowHooks_StartComplete(t *testing.T) {
	GlobalMetrics.Reset()

	runner := &mockToolRunner{results: map[string]string{"echo": "ok"}}
	engine, store := newTestEngine(t, runner)

	recorder := &hookRecorder{}
	engine.SetHooks(recorder)

	wf := NewWorkflow("wf1", "Hooks", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	events := recorder.getEvents()
	expected := []string{"workflow_started", "workflow_step_started", "workflow_step_completed", "workflow_completed"}
	if len(events) != len(expected) {
		t.Fatalf("events = %v, want %v", events, expected)
	}
	for i, e := range expected {
		if events[i] != e {
			t.Errorf("event[%d] = %q, want %q", i, events[i], e)
		}
	}

	if GlobalMetrics.HooksFired.Load() != int64(len(expected)) {
		t.Errorf("HooksFired = %d, want %d", GlobalMetrics.HooksFired.Load(), len(expected))
	}
}

func TestWorkflowHooks_StepFailed(t *testing.T) {
	runner := &mockToolRunner{err: errors.New("boom")}
	engine, store := newTestEngine(t, runner)

	recorder := &hookRecorder{}
	engine.SetHooks(recorder)

	wf := NewWorkflow("wf1", "FailHooks", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf1")

	events := recorder.getEvents()
	hasStepFailed := false
	hasWorkflowFailed := false
	for _, e := range events {
		if e == "workflow_step_failed" {
			hasStepFailed = true
		}
		if e == "workflow_failed" {
			hasWorkflowFailed = true
		}
	}
	if !hasStepFailed {
		t.Error("missing workflow_step_failed event")
	}
	if !hasWorkflowFailed {
		t.Error("missing workflow_failed event")
	}
}

func TestWorkflowHooks_Cancelled(t *testing.T) {
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	recorder := &hookRecorder{}
	engine.SetHooks(recorder)

	wf := NewWorkflow("wf1", "CancelHooks", "telegram:1", nil)
	wf.State = StateRunning
	_ = store.Save(wf)
	_ = engine.Cancel("wf1")

	events := recorder.getEvents()
	if len(events) != 1 || events[0] != "workflow_cancelled" {
		t.Errorf("events = %v, want [workflow_cancelled]", events)
	}
}

func TestWorkflowHooks_NilSafe(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"echo": "ok"}}
	engine, store := newTestEngine(t, runner)
	// Do NOT set hooks — should be nil-safe

	wf := NewWorkflow("wf1", "NilHooks", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	_ = store.Save(wf)
	// Should not panic
	_ = engine.Start(context.Background(), "wf1")

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

// --- Skill-aware LLM tests ---

type mockSkillResolver struct {
	skills map[string]string
}

func (r *mockSkillResolver) LoadSkill(name string) (string, bool) {
	s, ok := r.skills[name]
	return s, ok
}

func TestLLMExecutor_SkillNotFound(t *testing.T) {
	executor := NewLLMExecutor(nil)
	executor.SetSkills(&mockSkillResolver{skills: map[string]string{}})

	wf := NewWorkflow("wf1", "SkillLLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"skill": "nonexistent",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestLLMExecutor_NoSkillResolver(t *testing.T) {
	executor := NewLLMExecutor(nil)
	// Do NOT set skills resolver

	wf := NewWorkflow("wf1", "NoResolver", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"skill": "research",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error when no skill resolver configured")
	}
	if !strings.Contains(err.Error(), "no skill resolver") {
		t.Errorf("error = %q, want 'no skill resolver'", err.Error())
	}
}

func TestLLMExecutor_PromptFallback(t *testing.T) {
	// When no skill and no prompt, should error
	executor := NewLLMExecutor(nil)

	wf := NewWorkflow("wf1", "NoProm", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error for missing prompt and skill")
	}
	if !strings.Contains(err.Error(), "missing 'prompt' or 'skill'") {
		t.Errorf("error = %q, want mention of missing prompt or skill", err.Error())
	}
}

func TestLLMExecutor_SkillRef(t *testing.T) {
	provider := &realMockProvider{response: "skill output"}
	executor := NewLLMExecutor(provider)
	executor.SetSkills(&mockSkillResolver{skills: map[string]string{
		"research": "You are a researcher. Analyze the topic thoroughly.",
	}})

	wf := NewWorkflow("wf1", "SkillRef", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"skill": "research",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step.Result != "skill output" {
		t.Errorf("result = %q, want %q", step.Result, "skill output")
	}
	if wf.Context["s1"] != "skill output" {
		t.Errorf("context[s1] = %v, want %q", wf.Context["s1"], "skill output")
	}
	// Verify the prompt sent to provider was the skill prompt
	if !strings.Contains(provider.lastPrompt, "researcher") {
		t.Errorf("prompt sent = %q, want skill prompt", provider.lastPrompt)
	}
}

func TestLLMExecutor_SkillWithInput(t *testing.T) {
	provider := &realMockProvider{response: "researched"}
	executor := NewLLMExecutor(provider)
	executor.SetSkills(&mockSkillResolver{skills: map[string]string{
		"research": "Analyze this topic:",
	}})

	wf := NewWorkflow("wf1", "SkillInput", "telegram:1", nil)
	wf.Context["fetch"] = "raw data here"
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"skill": "research",
		"input": "Data: {{fetch}}",
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify input was appended with resolved refs
	if !strings.Contains(provider.lastPrompt, "Analyze this topic:") {
		t.Errorf("prompt missing skill text: %q", provider.lastPrompt)
	}
	if !strings.Contains(provider.lastPrompt, "raw data here") {
		t.Errorf("prompt missing resolved input: %q", provider.lastPrompt)
	}
}

// realMockProvider implements LLMProvider for tests that need full execution.
type realMockProvider struct {
	response   string
	lastPrompt string
}

func (m *realMockProvider) Chat(_ context.Context, msgs []LLMMessage, _ string) (*LLMResponse, error) {
	if len(msgs) > 0 {
		m.lastPrompt = msgs[len(msgs)-1].Content
	}
	return &LLMResponse{Content: m.response}, nil
}

func (m *realMockProvider) GetDefaultModel() string { return "test-model" }

// --- Engine SetAgentRunner / SetSkills / SetHooks nil safety ---

func TestEngine_SetAgentRunner_NilEngine(t *testing.T) {
	// SetAgentRunner should not panic even on a freshly created engine
	store := newTestStore(t)
	engine := &Engine{store: store, executors: map[StepKind]StepExecutor{}}
	engine.SetAgentRunner(&mockAgentRunner{result: "ok"})

	if _, ok := engine.executors[StepAgent]; !ok {
		t.Error("agent executor not registered")
	}
}

func TestEngine_SetSkills_NoLLMExecutor(t *testing.T) {
	store := newTestStore(t)
	engine := &Engine{store: store, executors: map[StepKind]StepExecutor{}}
	// Should not panic even without LLM executor
	engine.SetSkills(&mockSkillResolver{skills: map[string]string{"x": "y"}})
}

// --- Metrics summary ---

func TestMetrics_Summary(t *testing.T) {
	GlobalMetrics.Reset()
	GlobalMetrics.AgentStepsExecuted.Store(5)
	GlobalMetrics.HooksFired.Store(12)

	summary := GlobalMetrics.Summary()
	if !strings.Contains(summary, "Agent steps executed: 5") {
		t.Errorf("summary missing agent steps: %s", summary)
	}
	if !strings.Contains(summary, "Hooks fired: 12") {
		t.Errorf("summary missing hooks fired: %s", summary)
	}
}

// --- StartAsync + CompletionNotifier tests ---

func TestStartAsync_NonBlocking(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{results: map[string]string{"echo": "hello"}}
	engine, store := newTestEngine(t, runner)

	var notified sync.WaitGroup
	notified.Add(1)
	var notifiedWf *Workflow
	engine.SetCompletionNotifier(func(wf *Workflow) {
		notifiedWf = wf
		notified.Done()
	})

	wf := NewWorkflow("wf1", "Async", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	_ = store.Save(wf)

	// StartAsync should return immediately
	err := engine.StartAsync(context.Background(), "wf1")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for completion notification
	notified.Wait()

	if notifiedWf == nil {
		t.Fatal("completion notifier was not called")
	}
	if notifiedWf.State != StateCompleted {
		t.Errorf("notified state = %s, want completed", notifiedWf.State)
	}

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("workflow state = %s, want completed", loaded.State)
	}
}

func TestStartAsync_FailureNotifies(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{err: errors.New("boom")}
	engine, store := newTestEngine(t, runner)

	var notified sync.WaitGroup
	notified.Add(1)
	var notifiedState WorkflowState
	engine.SetCompletionNotifier(func(wf *Workflow) {
		notifiedState = wf.State
		notified.Done()
	})

	wf := NewWorkflow("wf1", "FailAsync", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.StartAsync(context.Background(), "wf1")

	notified.Wait()
	if notifiedState != StateFailed {
		t.Errorf("notified state = %s, want failed", notifiedState)
	}
}

func TestCancel_Notifies(t *testing.T) {
	GlobalMetrics.Reset()
	runner := &mockToolRunner{}
	engine, store := newTestEngine(t, runner)

	var notifiedState WorkflowState
	engine.SetCompletionNotifier(func(wf *Workflow) {
		notifiedState = wf.State
	})

	wf := NewWorkflow("wf1", "CancelMe", "telegram:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})
	wf.State = StateRunning
	_ = store.Save(wf)

	_ = engine.Cancel("wf1")

	if notifiedState != StateCancelled {
		t.Errorf("notified state = %s, want cancelled", notifiedState)
	}
}

func TestMetrics_Reset(t *testing.T) {
	GlobalMetrics.AgentStepsExecuted.Store(10)
	GlobalMetrics.AgentStepsFailed.Store(3)
	GlobalMetrics.HooksFired.Store(7)
	GlobalMetrics.Reset()

	if GlobalMetrics.AgentStepsExecuted.Load() != 0 {
		t.Error("AgentStepsExecuted not reset")
	}
	if GlobalMetrics.AgentStepsFailed.Load() != 0 {
		t.Error("AgentStepsFailed not reset")
	}
	if GlobalMetrics.HooksFired.Load() != 0 {
		t.Error("HooksFired not reset")
	}
}
