package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// === BUG: Metrics option ordering ===
// WithMetrics AFTER WithAgentRunner/WithLLMProvider/WithA2ACaller
// means executors get GlobalMetrics instead of the user-specified instance.

func TestMetricsOptionOrder_AgentRunnerBeforeMetrics(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	runner := &stressAgentRunner{result: "ok"}
	store := newTestStore(t)

	// WithAgentRunner BEFORE WithMetrics — executor should still use m
	engine := NewEngine(store,
		WithAgentRunner(runner),
		WithMetrics(m),
		WithToolRunner(&mockToolRunner{}),
	)

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "a", Kind: StepAgent, Config: map[string]any{"task": "do stuff"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	if m.AgentStepsExecuted.Load() != 1 {
		t.Errorf("agent metric not recorded on user metrics: got %d, want 1", m.AgentStepsExecuted.Load())
	}
}

func TestMetricsOptionOrder_LLMProviderBeforeMetrics(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	llm := &stressLLM{response: &LLMResponse{Content: "hello", InputTokens: 10, OutputTokens: 5}}
	store := newTestStore(t)

	// WithLLMProvider BEFORE WithMetrics
	engine := NewEngine(store,
		WithLLMProvider(llm),
		WithMetrics(m),
	)

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "a", Kind: StepLLM, Config: map[string]any{"prompt": "hi"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	if m.LLMTokensInput.Load() != 10 {
		t.Errorf("LLM input tokens not on user metrics: got %d, want 10", m.LLMTokensInput.Load())
	}
	if m.LLMTokensOutput.Load() != 5 {
		t.Errorf("LLM output tokens not on user metrics: got %d, want 5", m.LLMTokensOutput.Load())
	}
}

func TestMetricsOptionOrder_A2ACallerBeforeMetrics(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	caller := &stressA2ACaller{result: "ok"}
	store := newTestStore(t)

	// WithA2ACaller BEFORE WithMetrics
	engine := NewEngine(store,
		WithA2ACaller(caller),
		WithMetrics(m),
	)

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "a", Kind: StepA2A, Config: map[string]any{"agent_id": "remote", "message": "hi"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	if m.A2AStepsExecuted.Load() != 1 {
		t.Errorf("A2A metric not on user metrics: got %d, want 1", m.A2AStepsExecuted.Load())
	}
}

// Verify correct order works too (sanity check).
func TestMetricsOptionOrder_MetricsFirst_OK(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	runner := &stressAgentRunner{result: "ok"}
	store := newTestStore(t)

	engine := NewEngine(store,
		WithMetrics(m),
		WithAgentRunner(runner),
		WithToolRunner(&mockToolRunner{}),
	)

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "a", Kind: StepAgent, Config: map[string]any{"task": "do stuff"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	if m.AgentStepsExecuted.Load() != 1 {
		t.Errorf("agent metric not recorded: got %d, want 1", m.AgentStepsExecuted.Load())
	}
}

// === Clone deep copy correctness ===

func TestClone_ContextNestedMapIsolation(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID:    "wf1",
		State: StatePending,
		Context: map[string]any{
			"step1": map[string]any{
				"data": "original",
			},
		},
	}

	cloned := wf.Clone()

	// Mutate nested map in original
	original := wf.Context["step1"].(map[string]any)
	original["data"] = "mutated"
	original["extra"] = "added"

	// Clone should NOT be affected — Context uses maps.Clone (shallow!)
	clonedVal := cloned.Context["step1"]
	clonedNested, ok := clonedVal.(map[string]any)
	if !ok {
		t.Fatalf("cloned context value is not map[string]any: %T", clonedVal)
	}
	if clonedNested["data"] != "original" {
		t.Errorf("clone affected by original mutation: got %q, want %q", clonedNested["data"], "original")
	}
	if _, exists := clonedNested["extra"]; exists {
		t.Error("clone has key added to original after clone")
	}
}

func TestClone_StepConfigIsolation(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID:    "wf1",
		State: StatePending,
		Steps: []Step{
			{ID: "s1", Kind: StepTool, Config: map[string]any{
				"tool": "test",
				"args": map[string]any{"key": "value"},
			}},
		},
		Context: make(map[string]any),
	}

	cloned := wf.Clone()

	// Mutate original step config
	wf.Steps[0].Config["tool"] = "changed"
	nested := wf.Steps[0].Config["args"].(map[string]any)
	nested["key"] = "changed"

	// Clone should be unaffected (Config uses deepCloneMap via JSON)
	if cloned.Steps[0].Config["tool"] != "test" {
		t.Errorf("clone step config affected: got %q, want %q", cloned.Steps[0].Config["tool"], "test")
	}
	clonedArgs := cloned.Steps[0].Config["args"].(map[string]any)
	if clonedArgs["key"] != "value" {
		t.Errorf("clone nested config affected: got %q, want %q", clonedArgs["key"], "value")
	}
}

// === TransformExecutor combined operations ===

func TestTransformExecutor_PickThenOmit(t *testing.T) {
	t.Parallel()
	ex := NewTransformExecutor()
	wf := &Workflow{
		ID:      "wf1",
		Context: map[string]any{"src": map[string]any{"a": 1, "b": 2, "c": 3}},
	}
	step := &Step{
		ID:   "t",
		Kind: StepTransform,
		Config: map[string]any{
			"input": "src",
			"pick":  []any{"a", "b"},
			"omit":  []any{"b"},
		},
	}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if _, ok := result["a"]; !ok {
		t.Error("expected key 'a' to survive pick+omit")
	}
	if _, ok := result["b"]; ok {
		t.Error("expected key 'b' to be omitted after pick")
	}
	if _, ok := result["c"]; ok {
		t.Error("expected key 'c' to be removed by pick")
	}
}

func TestTransformExecutor_SetThenPickThenRename(t *testing.T) {
	t.Parallel()
	ex := NewTransformExecutor()
	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{
		ID:   "t",
		Kind: StepTransform,
		Config: map[string]any{
			"set":    map[string]any{"x": "1", "y": "2", "z": "3"},
			"pick":   []any{"x", "y"},
			"rename": map[string]any{"x": "alpha"},
		},
	}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if result["alpha"] != "1" {
		t.Errorf("expected alpha=1, got %v", result["alpha"])
	}
	if result["y"] != "2" {
		t.Errorf("expected y=2, got %v", result["y"])
	}
	if _, ok := result["x"]; ok {
		t.Error("x should have been renamed to alpha")
	}
	if _, ok := result["z"]; ok {
		t.Error("z should have been removed by pick")
	}
}

func TestTransformExecutor_MergeOverlappingKeys(t *testing.T) {
	t.Parallel()
	ex := NewTransformExecutor()
	wf := &Workflow{
		ID: "wf1",
		Context: map[string]any{
			"a": map[string]any{"key": "from_a", "only_a": true},
			"b": map[string]any{"key": "from_b", "only_b": true},
		},
	}
	step := &Step{
		ID:     "t",
		Kind:   StepTransform,
		Config: map[string]any{"merge": []any{"a", "b"}},
	}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if result["key"] != "from_b" {
		t.Errorf("expected key=from_b (last wins), got %v", result["key"])
	}
	if result["only_a"] != true {
		t.Error("expected only_a=true from merge")
	}
	if result["only_b"] != true {
		t.Error("expected only_b=true from merge")
	}
}

func TestTransformExecutor_EmptyConfig(t *testing.T) {
	t.Parallel()
	ex := NewTransformExecutor()
	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{ID: "t", Kind: StepTransform, Config: map[string]any{}}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if len(result) != 0 {
		t.Errorf("expected empty result for empty config, got %v", result)
	}
}

// === Concurrent metrics isolation ===

func TestConcurrentEngines_MetricsIsolation(t *testing.T) {
	t.Parallel()
	const N = 10
	allMetrics := make([]*Metrics, N)
	var wg sync.WaitGroup

	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m := NewMetrics()
			allMetrics[idx] = m
			store := newTestStore(t)
			engine := NewEngine(store,
				WithMetrics(m),
				WithToolRunner(&mockToolRunner{}),
			)

			for j := range 5 {
				wf := NewWorkflow(
					fmt.Sprintf("wf-%d-%d", idx, j), "test", "test:1",
					[]Step{{ID: "s", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending}},
				)
				_ = store.Save(wf)
				_ = engine.Start(context.Background(), wf.ID)
			}
		}(i)
	}
	wg.Wait()

	for i, m := range allMetrics {
		if m.WorkflowsCreated.Load() != 5 {
			t.Errorf("engine %d: WorkflowsCreated=%d, want 5", i, m.WorkflowsCreated.Load())
		}
		if m.StepsExecuted.Load() != 5 {
			t.Errorf("engine %d: StepsExecuted=%d, want 5", i, m.StepsExecuted.Load())
		}
	}
}

// === nil safety ===

func TestEngine_NilMetricsFallback(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	// Struct literal engine (no NewEngine) — metrics is nil
	engine := &Engine{
		store:     store,
		executors: map[StepKind]StepExecutor{StepTool: NewToolExecutor(&mockToolRunner{})},
	}

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "s", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
	})
	_ = store.Save(wf)

	// Should not panic — getMetrics() falls back to GlobalMetrics
	err := engine.Start(context.Background(), wf.ID)
	if err != nil {
		t.Fatalf("engine with nil metrics should work via fallback: %v", err)
	}
}

// === resolve.go edge cases ===

func TestResolveRefs_NestedBraces(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID:      "wf1",
		Context: map[string]any{"s1": "hello"},
	}

	got := ResolveRefs("prefix {{s1}} suffix", wf)
	if got != "prefix hello suffix" {
		t.Errorf("got %q, want %q", got, "prefix hello suffix")
	}

	got = ResolveRefs("{{unknown}}", wf)
	if got != "{{unknown}}" {
		t.Errorf("unknown ref should stay: got %q", got)
	}

	got = ResolveRefs("", wf)
	if got != "" {
		t.Errorf("empty string should stay empty: got %q", got)
	}
}

func TestResolveRef_StepsPrefix(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID:      "wf1",
		Context: map[string]any{"check": "value"},
	}

	got := resolveRef("$steps.check.result", wf)
	if got != "value" {
		t.Errorf("got %v, want %q", got, "value")
	}

	got = resolveRef("$steps.missing.result", wf)
	if got != "$steps.missing.result" {
		t.Errorf("missing ref should stay: got %v", got)
	}
}

// === stress test fakes (avoid collision with testhelpers_test.go names) ===

type stressAgentRunner struct {
	result string
	err    error
}

func (f *stressAgentRunner) RunTask(_ context.Context, _ string, _ string, _ AgentRunOpts) (string, error) {
	return f.result, f.err
}

type stressA2ACaller struct {
	result string
	err    error
}

func (f *stressA2ACaller) Call(_ context.Context, _, _ string) (string, error) {
	return f.result, f.err
}

type stressLLM struct {
	response *LLMResponse
	err      error
}

func (f *stressLLM) Chat(_ context.Context, _ []LLMMessage, _ string) (*LLMResponse, error) {
	return f.response, f.err
}

func (f *stressLLM) GetDefaultModel() string {
	return "test-model"
}
