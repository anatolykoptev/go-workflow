package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- Agent executor mocks ---

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

type taskCapturingRunner struct {
	inner    AgentRunner
	captured *string
}

func (r *taskCapturingRunner) RunTask(ctx context.Context, task string, sessionKey string, opts AgentRunOpts) (string, error) {
	*r.captured = task
	return r.inner.RunTask(ctx, task, sessionKey, opts)
}

// --- Agent executor tests ---

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

func TestWorkflowWithAgentStep(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"read_file": "file contents"}}
	engine, store := newTestEngine(t, runner)

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

// --- A2A executor mocks ---

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

type msgCapturingCaller struct {
	inner    A2ACaller
	captured *string
}

func (c *msgCapturingCaller) Call(ctx context.Context, agentID, message string) (string, error) {
	*c.captured = message
	return c.inner.Call(ctx, agentID, message)
}

// --- A2A executor tests ---

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

// --- LLM executor mocks ---

type mockSkillResolver struct {
	skills map[string]string
}

func (r *mockSkillResolver) LoadSkill(name string) (string, bool) {
	s, ok := r.skills[name]
	return s, ok
}

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

// --- LLM executor tests ---

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
	if !strings.Contains(provider.lastPrompt, "Analyze this topic:") {
		t.Errorf("prompt missing skill text: %q", provider.lastPrompt)
	}
	if !strings.Contains(provider.lastPrompt, "raw data here") {
		t.Errorf("prompt missing resolved input: %q", provider.lastPrompt)
	}
}

// --- Engine setter nil-safety tests ---

func TestEngine_SetAgentRunner_NilEngine(t *testing.T) {
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
	engine.SetSkills(&mockSkillResolver{skills: map[string]string{"x": "y"}})
}
