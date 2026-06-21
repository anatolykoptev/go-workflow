package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go-kit/llm"
)

const defaultMaxTurns = 10

// executeToolLoop runs the multi-turn tool calling loop.
// Stores final content in wf.Context[step.ID] and tool call log in wf.Context[step.ID+"_tool_calls"].
func (e *LLMExecutor) executeToolLoop(
	ctx context.Context, step *Step, wf *Workflow,
	messages []llm.Message, tools []llm.Tool, maxTurns int,
) error {
	model, _ := step.Config["model"].(string)
	var totalUsage llm.Usage
	var toolLog []map[string]any

	for turn := range maxTurns {
		var resp *llm.ChatResponse
		if breakErr := e.breakers.call("llm:"+model, func() error {
			var callErr error
			resp, callErr = e.client.Chat(ctx, messages, llm.WithTools(tools))
			return callErr
		}); breakErr != nil {
			return fmt.Errorf("llm turn %d: %w", turn+1, breakErr)
		}
		accumulateUsage(&totalUsage, resp.Usage)

		// No tool calls — final response
		if len(resp.ToolCalls) == 0 {
			step.Result = resp.Content
			wf.Context[step.ID] = resp.Content
			if len(toolLog) > 0 {
				wf.Context[step.ID+"_tool_calls"] = toolLog
			}
			recordUsage(step.ID, wf, e.metrics, totalUsage.PromptTokens, totalUsage.CompletionTokens, "")
			if e.engine != nil {
				if costErr := e.engine.recordStepCost(wf, StepCost{
					StepID:       step.ID,
					Kind:         StepLLM,
					Model:        model,
					InputTokens:  int64(totalUsage.PromptTokens),
					OutputTokens: int64(totalUsage.CompletionTokens),
				}); costErr != nil {
					return costErr
				}
			}
			return nil
		}

		// Append assistant message with tool calls
		messages = append(messages, llm.Message{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call and log results
		for _, tc := range resp.ToolCalls {
			result, toolErr := e.executeTool(ctx, tc, wf)
			if toolErr != nil {
				result = fmt.Sprintf("error: %s", toolErr)
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
			toolLog = append(toolLog, map[string]any{
				"id":     tc.ID,
				"name":   tc.Function.Name,
				"args":   tc.Function.Arguments,
				"result": result,
			})
		}
	}

	if len(toolLog) > 0 {
		wf.Context[step.ID+"_tool_calls"] = toolLog
	}
	return fmt.Errorf("step %s: max_turns (%d) exceeded", step.ID, maxTurns)
}

// executeTool checks security policy, parses arguments, and delegates to the tool runner.
// The outbound call is wrapped in the shared circuit breaker (keyed "tool:<name>") so
// a dead tool endpoint hit through the LLM tool-loop fails fast instead of being
// hammered on every turn.
func (e *LLMExecutor) executeTool(ctx context.Context, tc llm.ToolCall, wf *Workflow) (string, error) {
	// Enforce security policy for LLM-driven tool calls
	if wf.Security != nil && !wf.Security.IsToolAllowed(tc.Function.Name) {
		return "", fmt.Errorf("tool %q not permitted by security policy", tc.Function.Name)
	}

	var args map[string]any
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"raw": tc.Function.Arguments}
		}
	}

	var result string
	if err := e.breakers.call("tool:"+tc.Function.Name, func() error {
		var callErr error
		result, callErr = e.toolRunner.Execute(ctx, tc.Function.Name, args)
		return callErr
	}); err != nil {
		return "", err
	}
	return result, nil
}

// parseTools extracts tool definitions from step config.
func (e *LLMExecutor) parseTools(cfg map[string]any) []llm.Tool {
	arr, ok := cfg["tools"].([]any)
	if !ok {
		return nil
	}
	tools := make([]llm.Tool, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		desc, _ := m["description"].(string)
		params := m["parameters"]
		if name != "" {
			tools = append(tools, llm.NewTool(name, desc, params))
		}
	}
	return tools
}

// parseMaxTurns extracts the max_turns limit from step config (default 10).
func (e *LLMExecutor) parseMaxTurns(cfg map[string]any) int {
	v, _ := cfg["max_turns"].(float64)
	if v <= 0 {
		return defaultMaxTurns
	}
	return int(v)
}

// accumulateUsage adds API usage from one response to the running total.
func accumulateUsage(total *llm.Usage, add *llm.Usage) {
	if add == nil {
		return
	}
	total.PromptTokens += add.PromptTokens
	total.CompletionTokens += add.CompletionTokens
	total.TotalTokens += add.TotalTokens
}
