package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// StepExecutor runs a single step within a workflow.
type StepExecutor interface {
	Execute(ctx context.Context, step *Step, wf *Workflow) error
}

// ToolRunner is the interface for executing tools (satisfied by tools.ToolRegistry).
type ToolRunner interface {
	Execute(ctx context.Context, name string, args map[string]any) (string, error)
}

// ToolExecutor calls a registered tool and stores the result in workflow context.
type ToolExecutor struct {
	runner ToolRunner
}

func NewToolExecutor(runner ToolRunner) *ToolExecutor {
	return &ToolExecutor{runner: runner}
}

func (e *ToolExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	toolName, _ := step.Config["tool"].(string)
	if toolName == "" {
		return fmt.Errorf("step %s: missing 'tool' in config", step.ID)
	}

	args := make(map[string]any)
	if a, ok := step.Config["args"].(map[string]any); ok {
		// Resolve context references: "$steps.{id}.result" → actual value
		for k, v := range a {
			args[k] = resolveRef(v, wf)
		}
	}

	result, err := e.runner.Execute(ctx, toolName, args)
	if err != nil {
		return fmt.Errorf("tool %s: %w", toolName, err)
	}

	step.Result = result
	wf.Context[step.ID] = result
	return nil
}

// SkillResolver loads a skill prompt by name. Satisfied by skills.SkillsLoader.
type SkillResolver interface {
	LoadSkill(name string) (string, bool)
}

// LLMExecutor sends a prompt to the LLM provider and stores the response.
// Supports skill references: {"skill": "name", "input": "..."}.
type LLMExecutor struct {
	provider LLMProvider
	skills   SkillResolver
}

func NewLLMExecutor(provider LLMProvider) *LLMExecutor {
	return &LLMExecutor{provider: provider}
}

// SetSkills sets the skill resolver for skill-aware LLM steps. Nil-safe.
func (e *LLMExecutor) SetSkills(sr SkillResolver) {
	e.skills = sr
}

func (e *LLMExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	prompt, _ := step.Config["prompt"].(string)

	// Skill reference takes precedence over inline prompt
	if skillName, ok := step.Config["skill"].(string); ok && skillName != "" {
		if e.skills == nil {
			return fmt.Errorf("step %s: skill %q requested but no skill resolver configured", step.ID, skillName)
		}
		skillPrompt, found := e.skills.LoadSkill(skillName)
		if !found {
			return fmt.Errorf("step %s: skill %q not found", step.ID, skillName)
		}
		prompt = skillPrompt
		if input, ok := step.Config["input"].(string); ok && input != "" {
			prompt += "\n\n" + resolvePromptRefs(input, wf)
		}
	}

	if prompt == "" {
		return fmt.Errorf("step %s: missing 'prompt' or 'skill' in config", step.ID)
	}

	// Resolve context references in prompt
	prompt = resolvePromptRefs(prompt, wf)

	model, _ := step.Config["model"].(string)
	if model == "" {
		model = e.provider.GetDefaultModel()
	}

	messages := []LLMMessage{
		{Role: "user", Content: prompt},
	}

	resp, err := e.provider.Chat(ctx, messages, model)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	step.Result = resp.Content
	wf.Context[step.ID] = resp.Content

	if resp.InputTokens > 0 || resp.OutputTokens > 0 {
		wf.Context[step.ID+"_usage"] = map[string]any{
			"input_tokens":  resp.InputTokens,
			"output_tokens": resp.OutputTokens,
			"model":         resp.Model,
		}
		GlobalMetrics.LLMTokensInput.Add(int64(resp.InputTokens))
		GlobalMetrics.LLMTokensOutput.Add(int64(resp.OutputTokens))
	}
	return nil
}

// MessageExecutor publishes a message to the bus for delivery to the user.
type MessageExecutor struct {
	bus MessagePublisher
}

func NewMessageExecutor(bus MessagePublisher) *MessageExecutor {
	return &MessageExecutor{bus: bus}
}

func (e *MessageExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	content, _ := step.Config["content"].(string)
	if content == "" {
		// Use result from a referenced step
		if ref, ok := step.Config["content_from"].(string); ok {
			if val, ok := wf.Context[ref]; ok {
				content = fmt.Sprintf("%v", val)
			}
		}
	}

	if content == "" {
		return fmt.Errorf("step %s: missing 'content' or 'content_from' in config", step.ID)
	}

	content = resolvePromptRefs(content, wf)

	// Parse owner (format: "channel:chatID")
	channel, chatID := ParseOwner(wf.Owner)
	if channel == "" || chatID == "" {
		return fmt.Errorf("step %s: invalid owner format %q (expected channel:chatID)", step.ID, wf.Owner)
	}

	e.bus.PublishOutbound(OutboundMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: content,
	})

	step.Result = "delivered"
	wf.Context[step.ID] = "delivered"
	return nil
}

// ConditionExecutor evaluates a simple expression and skips or proceeds.
// Config: {"check": "stepID", "contains": "keyword"}
//
// Operators: "contains", "equals", "not_empty" (bool).
//
//	"not_empty" treats "", "<nil>", "[]", "[ ]", "{}", and "null" as empty.
//
// If "stop_workflow" is true and the condition evaluates to false,
// the step returns an error which stops the workflow immediately
// instead of silently passing "false" to downstream steps.
type ConditionExecutor struct{}

func NewConditionExecutor() *ConditionExecutor {
	return &ConditionExecutor{}
}

func (e *ConditionExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	check, _ := step.Config["check"].(string)
	if check == "" {
		return fmt.Errorf("step %s: missing 'check' in config", step.ID)
	}

	val := fmt.Sprintf("%v", resolveRef(check, wf))

	passed := false

	if contains, ok := step.Config["contains"].(string); ok {
		passed = strings.Contains(strings.ToLower(val), strings.ToLower(contains))
	} else if equals, ok := step.Config["equals"].(string); ok {
		passed = strings.EqualFold(val, equals)
	} else if notEmpty, ok := step.Config["not_empty"].(bool); ok && notEmpty {
		passed = !isEmptyValue(val)
	} else {
		return fmt.Errorf("step %s: condition needs 'contains', 'equals', or 'not_empty'", step.ID)
	}

	if passed {
		step.Result = "true" //nolint:goconst
		wf.Context[step.ID] = "true"
	} else {
		step.Result = "false" //nolint:goconst
		wf.Context[step.ID] = "false"

		// stop_workflow: fail immediately instead of passing "false" downstream
		if stop, ok := step.Config["stop_workflow"].(bool); ok && stop {
			msg, _ := step.Config["message"].(string)
			if msg == "" {
				msg = fmt.Sprintf("condition %q not met", step.ID)
			}
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}

// isEmptyValue returns true for strings that represent empty/nil data.
func isEmptyValue(s string) bool {
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "", "<nil>", "[]", "{}", "null", "[ ]", "{ }":
		return true
	}
	return false
}

// ApprovalExecutor transitions the workflow to waiting_approval state.
// Actual approval handling is done externally via Engine.HandleApproval.
type ApprovalExecutor struct{}

func NewApprovalExecutor() *ApprovalExecutor {
	return &ApprovalExecutor{}
}

func (e *ApprovalExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	// Signal that this workflow needs approval.
	// The engine will catch this and pause the workflow.
	return errApprovalRequired
}

// errApprovalRequired is a sentinel error used by ApprovalExecutor to signal the engine.
var errApprovalRequired = errors.New("approval required")

// SubWorkflowExecutor runs another workflow as a child step.
// Config: {"workflow_id": "child-id"}
// The child workflow must already exist in the store. The parent step
// blocks until the child completes. Child results are available in the
// parent context under the step ID.
type SubWorkflowExecutor struct {
	runner SubWorkflowRunner
}

// SubWorkflowRunner is the interface for running sub-workflows (satisfied by Engine).
type SubWorkflowRunner interface {
	Store() *WorkflowStore
	Start(ctx context.Context, workflowID string) error
	RunToCompletion(ctx context.Context, workflowID string) error
}

func NewSubWorkflowExecutor(runner SubWorkflowRunner) *SubWorkflowExecutor {
	return &SubWorkflowExecutor{runner: runner}
}

func (e *SubWorkflowExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	childID, _ := step.Config["workflow_id"].(string)
	if childID == "" {
		return fmt.Errorf("step %s: missing 'workflow_id' in config", step.ID)
	}

	child, ok := e.runner.Store().Load(childID)
	if !ok {
		return fmt.Errorf("step %s: child workflow %s not found", step.ID, childID)
	}

	// Start child if pending
	switch child.State {
	case StatePending:
		if err := e.runner.Start(ctx, childID); err != nil {
			return fmt.Errorf("step %s: start child %s: %w", step.ID, childID, err)
		}
	case StateRunning, StatePaused:
		// Resume running child
		if err := e.runner.RunToCompletion(ctx, childID); err != nil {
			return fmt.Errorf("step %s: resume child %s: %w", step.ID, childID, err)
		}
	}

	// Check child final state
	child, ok = e.runner.Store().Load(childID)
	if !ok {
		return fmt.Errorf("step %s: child workflow %s disappeared", step.ID, childID)
	}
	if child.State == StateCompleted {
		step.Result = child.Context
		wf.Context[step.ID] = child.Context
		return nil
	}

	if child.State == StateWaitingApproval {
		return fmt.Errorf("step %s: child workflow %s is waiting for approval", step.ID, childID)
	}

	return fmt.Errorf("step %s: child workflow %s ended with state %s: %s", step.ID, childID, child.State, child.Error)
}

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
	runner AgentRunner
}

func NewAgentExecutor(runner AgentRunner) *AgentExecutor {
	return &AgentExecutor{runner: runner}
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

	result, err := e.runner.RunTask(ctx, task, sessionKey, opts)
	if err != nil {
		GlobalMetrics.AgentStepsFailed.Add(1)
		return fmt.Errorf("agent: %w", err)
	}

	GlobalMetrics.AgentStepsExecuted.Add(1)
	step.Result = result
	wf.Context[step.ID] = result
	return nil
}

// A2ACaller is the interface for calling remote A2A agents.
// Satisfied by *a2a.ClientManager (which has Call(ctx, agentID, message)).
type A2ACaller interface {
	Call(ctx context.Context, agentID, message string) (string, error)
}

// A2AExecutor delegates a step to a remote A2A agent.
type A2AExecutor struct {
	caller A2ACaller
}

func NewA2AExecutor(caller A2ACaller) *A2AExecutor {
	return &A2AExecutor{caller: caller}
}

func (e *A2AExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	agentID, _ := step.Config["agent_id"].(string)
	if agentID == "" {
		return fmt.Errorf("step %s: missing 'agent_id' in config", step.ID)
	}

	message, _ := step.Config["message"].(string)
	if message == "" {
		return fmt.Errorf("step %s: missing 'message' in config", step.ID)
	}

	message = resolvePromptRefs(message, wf)

	// Apply per-call timeout override if configured
	if timeoutSec, ok := step.Config["timeout_seconds"].(float64); ok && timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec*float64(time.Second)))
		defer cancel()
	}

	result, err := e.caller.Call(ctx, agentID, message)
	if err != nil {
		GlobalMetrics.A2AStepsFailed.Add(1)
		return fmt.Errorf("a2a %s: %w", agentID, err)
	}

	GlobalMetrics.A2AStepsExecuted.Add(1)
	step.Result = result
	wf.Context[step.ID] = result
	return nil
}

// TransformExecutor performs lightweight JSON data transformations.
// Config operations:
//   - "set": map of key→value pairs to set (supports {{stepID}} refs)
//   - "pick": array of keys to keep from input
//   - "omit": array of keys to remove from input
//   - "rename": map of old_key→new_key
//   - "input": step ID to use as input data (default: previous step)
//   - "merge": array of step IDs whose results are merged into one object
//
// This is much cheaper than agent delegation for simple data routing.
type TransformExecutor struct{}

func NewTransformExecutor() *TransformExecutor {
	return &TransformExecutor{}
}

func (e *TransformExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	// Start with input data
	result := make(map[string]any)

	// Determine input source
	if inputRef, ok := step.Config["input"].(string); ok {
		if v, ok := wf.Context[inputRef]; ok {
			if m, ok := v.(map[string]any); ok {
				for k, v := range m {
					result[k] = v
				}
			} else {
				result["_input"] = v
			}
		}
	}

	// Merge: combine multiple step results into one object
	if mergeRefs, ok := step.Config["merge"].([]any); ok {
		for _, ref := range mergeRefs {
			refStr, ok := ref.(string)
			if !ok {
				continue
			}
			if v, ok := wf.Context[refStr]; ok {
				if m, ok := v.(map[string]any); ok {
					for k, v := range m {
						result[k] = v
					}
				} else {
					result[refStr] = v
				}
			}
		}
	}

	// Set: add/overwrite key-value pairs
	if setMap, ok := step.Config["set"].(map[string]any); ok {
		for k, v := range setMap {
			if s, ok := v.(string); ok {
				result[k] = resolvePromptRefs(s, wf)
			} else {
				result[k] = v
			}
		}
	}

	// Pick: keep only specified keys
	if pickArr, ok := step.Config["pick"].([]any); ok {
		picked := make(map[string]any)
		for _, k := range pickArr {
			if key, ok := k.(string); ok {
				if v, exists := result[key]; exists {
					picked[key] = v
				}
			}
		}
		result = picked
	}

	// Omit: remove specified keys
	if omitArr, ok := step.Config["omit"].([]any); ok {
		for _, k := range omitArr {
			if key, ok := k.(string); ok {
				delete(result, key)
			}
		}
	}

	// Rename: rename keys
	if renameMap, ok := step.Config["rename"].(map[string]any); ok {
		for oldKey, newKeyRaw := range renameMap {
			if newKey, ok := newKeyRaw.(string); ok {
				if v, exists := result[oldKey]; exists {
					result[newKey] = v
					delete(result, oldKey)
				}
			}
		}
	}

	step.Result = result
	wf.Context[step.ID] = result
	return nil
}

// resolveRef replaces "$steps.{id}" references with context values.
func resolveRef(val any, wf *Workflow) any {
	s, ok := val.(string)
	if !ok {
		return val
	}

	// "$steps.check.result" → context["check"]
	if strings.HasPrefix(s, "$steps.") {
		parts := strings.SplitN(s[7:], ".", 2) // strip "$steps."
		stepID := parts[0]
		if v, ok := wf.Context[stepID]; ok {
			return v
		}
		return s
	}

	// "{{stepID}}" → context["stepID"] (whole-value replacement)
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") && strings.Count(s, "{{") == 1 {
		stepID := strings.TrimSpace(s[2 : len(s)-2])
		if v, ok := wf.Context[stepID]; ok {
			return v
		}
	}

	// Inline {{stepID}} references within a larger string
	if strings.Contains(s, "{{") {
		return ResolveRefs(s, wf)
	}

	return s
}

// resolvePromptRefs replaces {{stepID}} placeholders in a string with context values.
func resolvePromptRefs(s string, wf *Workflow) string {
	return ResolveRefs(s, wf)
}

// ResolveRefs replaces {{stepID}} placeholders in a string with workflow context values.
// Also resolves ${VAR} and $env.VAR patterns with environment variables (n8n compat).
func ResolveRefs(s string, wf *Workflow) string {
	for id, val := range wf.Context {
		s = strings.ReplaceAll(s, "{{"+id+"}}", fmt.Sprintf("%v", val))
	}
	// Resolve ${VAR} environment variable references (n8n compat)
	s = resolveEnvVars(s)
	return s
}

// envVarRegex matches ${VAR_NAME} patterns.
var envVarRegex = regexp.MustCompile(`\$\{(\w+)\}`)

// resolveEnvVars replaces ${VAR} patterns with os.Getenv values.
// Only replaces if the env var is actually set (non-empty).
func resolveEnvVars(s string) string {
	return envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		groups := envVarRegex.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if val := os.Getenv(groups[1]); val != "" {
			return val
		}
		return match // keep placeholder if env var not set
	})
}

// ParseOwner splits "channel:chatID" into parts.
func ParseOwner(owner string) (channel, chatID string) {
	idx := strings.Index(owner, ":")
	if idx <= 0 {
		return "", ""
	}
	return owner[:idx], owner[idx+1:]
}
