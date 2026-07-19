package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/llm"
)

// SkillResolver loads a skill prompt by name. Satisfied by skills.SkillsLoader.
type SkillResolver interface {
	LoadSkill(name string) (string, bool)
}

// StreamCallback receives streaming LLM chunks during execution.
type StreamCallback func(workflowID, stepID, delta string)

// LLMExecutor sends a prompt to the LLM provider and stores the response.
// Supports both legacy LLMProvider interface and go-kit *llm.Client (preferred).
// Supports skill references: {"skill": "name", "input": "..."}.
// Supports multi-turn tool calling when tools are configured and a ToolRunner is set.
type LLMExecutor struct {
	provider   LLMProvider // legacy interface
	client     *llm.Client // go-kit client (preferred)
	skills     SkillResolver
	metrics    *Metrics
	streamCB   StreamCallback
	toolRunner ToolRunner
	engine     *Engine          // back-reference for cost recording (set by NewEngine)
	breakers   *breakerRegistry // nil = disabled (e.g. in unit tests)
}

// NewLLMExecutor creates an LLMExecutor using the legacy LLMProvider interface.
func NewLLMExecutor(provider LLMProvider, metrics *Metrics) *LLMExecutor {
	return &LLMExecutor{provider: provider, metrics: metrics}
}

// NewLLMExecutorWithClient creates an LLMExecutor using the go-kit LLM client.
func NewLLMExecutorWithClient(client *llm.Client, metrics *Metrics) *LLMExecutor {
	return &LLMExecutor{client: client, metrics: metrics}
}

// SetSkills sets the skill resolver for skill-aware LLM steps. Nil-safe.
func (e *LLMExecutor) SetSkills(sr SkillResolver) { e.skills = sr }

// SetStreamCallback sets the callback for streaming LLM chunks.
func (e *LLMExecutor) SetStreamCallback(cb StreamCallback) { e.streamCB = cb }

// SetToolRunner sets the tool runner for multi-turn tool calling.
func (e *LLMExecutor) SetToolRunner(tr ToolRunner) { e.toolRunner = tr }

func (e *LLMExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	prompt, err := resolvePrompt(step, wf, e.skills)
	if err != nil {
		return err
	}
	prompt = resolvePromptRefs(prompt, wf)

	if e.client != nil {
		return e.executeClient(ctx, step, wf, prompt)
	}
	return e.executeProvider(ctx, step, wf, prompt)
}

// executeProvider runs the legacy LLMProvider path.
func (e *LLMExecutor) executeProvider(ctx context.Context, step *Step, wf *Workflow, prompt string) error {
	model, _ := step.Config["model"].(string)
	if model == "" {
		model = e.provider.GetDefaultModel()
	}
	messages := []LLMMessage{{Role: "user", Content: prompt}}

	start := time.Now()
	var resp *LLMResponse
	if err := e.breakers.call("llm:"+model, func() error {
		var callErr error
		resp, callErr = e.provider.Chat(ctx, messages, model)
		return callErr
	}); err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	step.Result = resp.Content
	wf.Context[step.ID] = resp.Content
	recordUsage(step.ID, wf, e.metrics, resp.InputTokens, resp.OutputTokens, resp.Model)
	if e.engine != nil {
		respModel := resp.Model
		if respModel == "" {
			respModel = model
		}
		if costErr := e.engine.recordStepCost(wf, StepCost{
			StepID:       step.ID,
			Kind:         StepLLM,
			Model:        respModel,
			InputTokens:  int64(resp.InputTokens),
			OutputTokens: int64(resp.OutputTokens),
			DurationMS:   time.Since(start).Milliseconds(),
		}); costErr != nil {
			return costErr
		}
	}
	return nil
}

// executeClient runs the go-kit client path with optional streaming and tool calling.
func (e *LLMExecutor) executeClient(ctx context.Context, step *Step, wf *Workflow, prompt string) error {
	msgs := []llm.Message{{Role: "user", Content: prompt}}

	tools := e.parseTools(step.Config)
	maxTurns := e.parseMaxTurns(step.Config)

	if len(tools) > 0 && e.toolRunner != nil {
		return e.executeToolLoop(ctx, step, wf, msgs, tools, maxTurns)
	}

	wantStream, _ := step.Config["stream"].(bool)
	if wantStream && e.streamCB != nil {
		return e.executeStream(ctx, step, wf, msgs)
	}

	start := time.Now()
	clientModel, _ := step.Config["model"].(string)
	var resp *llm.ChatResponse
	if err := e.breakers.call("llm:"+clientModel, func() error {
		var callErr error
		resp, callErr = e.client.Chat(ctx, msgs)
		return callErr
	}); err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	step.Result = resp.Content
	wf.Context[step.ID] = resp.Content
	if resp.Usage != nil {
		recordUsage(step.ID, wf, e.metrics, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, "")
	}
	if e.engine != nil && resp.Usage != nil {
		model, _ := step.Config["model"].(string)
		if costErr := e.engine.recordStepCost(wf, StepCost{
			StepID:       step.ID,
			Kind:         StepLLM,
			Model:        model,
			InputTokens:  int64(resp.Usage.PromptTokens),
			OutputTokens: int64(resp.Usage.CompletionTokens),
			DurationMS:   time.Since(start).Milliseconds(),
		}); costErr != nil {
			return costErr
		}
	}
	return nil
}

// executeStream handles the streaming path via go-kit client.
func (e *LLMExecutor) executeStream(ctx context.Context, step *Step, wf *Workflow, msgs []llm.Message) error {
	streamModel, _ := step.Config["model"].(string)
	var buf strings.Builder
	var streamUsage *llm.Usage
	streamErr := e.breakers.call("llm:"+streamModel, func() error {
		stream, err := e.client.Stream(ctx, msgs)
		if err != nil {
			return fmt.Errorf("llm stream: %w", err)
		}
		defer stream.Close()

		for {
			chunk, ok := stream.Next()
			if !ok {
				break
			}
			buf.WriteString(chunk.Delta)
			e.streamCB(wf.ID, step.ID, chunk.Delta)
		}
		if err := stream.Err(); err != nil {
			return fmt.Errorf("llm stream: %w", err)
		}
		streamUsage = stream.Usage()
		return nil
	})
	if streamErr != nil {
		return streamErr
	}

	content := buf.String()
	step.Result = content
	wf.Context[step.ID] = content
	if u := streamUsage; u != nil {
		recordUsage(step.ID, wf, e.metrics, u.PromptTokens, u.CompletionTokens, "")
		if e.engine != nil {
			model, _ := step.Config["model"].(string)
			if costErr := e.engine.recordStepCost(wf, StepCost{
				StepID:       step.ID,
				Kind:         StepLLM,
				Model:        model,
				InputTokens:  int64(u.PromptTokens),
				OutputTokens: int64(u.CompletionTokens),
			}); costErr != nil {
				return costErr
			}
		}
	}
	return nil
}

// resolvePrompt extracts the prompt from step config, handling skill references.
func resolvePrompt(step *Step, wf *Workflow, skills SkillResolver) (string, error) {
	prompt, _ := step.Config["prompt"].(string)

	skillName, ok := step.Config["skill"].(string)
	if !ok || skillName == "" {
		if prompt == "" {
			return "", fmt.Errorf("step %s: missing 'prompt' or 'skill' in config", step.ID)
		}
		return prompt, nil
	}
	if skills == nil {
		return "", fmt.Errorf("step %s: skill %q requested but no skill resolver configured", step.ID, skillName)
	}
	skillPrompt, found := skills.LoadSkill(skillName)
	if !found {
		return "", fmt.Errorf("step %s: skill %q not found", step.ID, skillName)
	}
	prompt = skillPrompt
	if input, ok := step.Config["input"].(string); ok && input != "" {
		prompt += "\n\n" + resolvePromptRefs(input, wf)
	}
	return prompt, nil
}

// recordUsage stores token usage in workflow context and updates metrics.
func recordUsage(stepID string, wf *Workflow, m *Metrics, input, output int, model string) {
	if input <= 0 && output <= 0 {
		return
	}
	wf.Context[stepID+"_usage"] = map[string]any{
		"input_tokens":  input,
		"output_tokens": output,
		"model":         model,
	}
	m.LLMTokensInput.Add(int64(input))
	m.LLMTokensOutput.Add(int64(output))
}
