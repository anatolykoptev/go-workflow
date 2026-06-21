package workflow

import (
	"context"
	"fmt"
)

// AgentRunOpts configures an agent step execution.
type AgentRunOpts struct {
	Model          string
	TimeoutSeconds int
	MaxIterations  int
	SkipContext    bool // skip MemDB context fetch (default true for workflow steps)
}

// AgentRunner is the interface for delegating tasks to the full agent loop.
// Satisfied by agent.WorkflowAgentAdapter.
type AgentRunner interface {
	RunTask(ctx context.Context, task string, sessionKey string, opts AgentRunOpts) (string, error)
}

// AgentExecutor delegates a task to the full agent loop (with tools, memory, skills).
type AgentExecutor struct {
	runner   AgentRunner
	metrics  *Metrics
	breakers *breakerRegistry // nil = disabled (e.g. in unit tests)
}

func NewAgentExecutor(runner AgentRunner, metrics *Metrics) *AgentExecutor {
	return &AgentExecutor{runner: runner, metrics: metrics}
}

func (e *AgentExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	task, _ := step.Config["task"].(string)
	if task == "" {
		return fmt.Errorf("step %s: missing 'task' in config", step.ID)
	}

	task = resolvePromptRefs(task, wf)

	model, _ := step.Config["model"].(string)
	timeoutSec := 0
	if v, ok := step.Config["timeout_seconds"].(float64); ok {
		timeoutSec = int(v)
	}
	maxIter := 0
	if v, ok := step.Config["max_iterations"].(float64); ok {
		maxIter = int(v)
	}

	// Use isolated session key per step to prevent history pollution.
	// If the step shares the owner's session, the LLM sees old conversation
	// history and may hallucinate tool results instead of actually calling tools.
	sessionKey := fmt.Sprintf("wf:%s:%s", wf.ID, step.ID)
	if sk, ok := step.Config["session_key"].(string); ok && sk != "" {
		sessionKey = resolvePromptRefs(sk, wf)
	}

	// By default, workflow agent steps skip MemDB context fetch to avoid
	// expensive ONNX embedding + search on every step. Steps can opt-in
	// with inject_context: true in their config.
	skipCtx := true
	if v, ok := step.Config["inject_context"].(bool); ok && v {
		skipCtx = false
	}

	opts := AgentRunOpts{
		Model:          model,
		TimeoutSeconds: timeoutSec,
		MaxIterations:  maxIter,
		SkipContext:    skipCtx,
	}

	var result string
	err := e.breakers.call("agent:"+step.ID, func() error {
		var callErr error
		result, callErr = e.runner.RunTask(ctx, task, sessionKey, opts)
		return callErr
	})
	if err != nil {
		e.metrics.AgentStepsFailed.Add(1)
		return fmt.Errorf("agent: %w", err)
	}

	e.metrics.AgentStepsExecuted.Add(1)
	step.Result = result
	wf.Context[step.ID] = result
	return nil
}
