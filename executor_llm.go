package workflow

import (
	"context"
	"fmt"
)

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
