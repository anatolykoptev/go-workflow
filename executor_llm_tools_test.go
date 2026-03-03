package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-kit/llm"
)

// toolCallJSON returns an OpenAI chat response with tool_calls (no content).
func toolCallJSON(toolCalls []map[string]any, promptTok, completionTok int) []byte {
	resp := map[string]any{
		"choices": []map[string]any{
			{
				"message": map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": toolCalls,
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func TestLLMExecutor_ToolCalling_SingleTurn(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")

		if n == 1 {
			// First call: return a tool call
			tc := []map[string]any{
				{
					"id":   "call_1",
					"type": "function",
					"function": map[string]string{
						"name":      "echo",
						"arguments": `{"text":"hello"}`,
					},
				},
			}
			_, _ = w.Write(toolCallJSON(tc, 20, 10))
			return
		}

		// Second call: return final content
		_, _ = w.Write(chatJSON("The echo says: hello", 30, 15))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	m := NewMetrics()
	executor := NewLLMExecutorWithClient(client, m)
	executor.SetToolRunner(&mockToolRunner{results: map[string]string{"echo": "hello"}})

	wf := NewWorkflow("wf1", "ToolLLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "echo hello",
		"tools": []any{
			map[string]any{
				"name":        "echo",
				"description": "echoes text",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			},
		},
	}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if callCount.Load() != 2 {
		t.Errorf("API call count = %d, want 2", callCount.Load())
	}

	result, _ := step.Result.(string)
	if result != "The echo says: hello" {
		t.Errorf("result = %q, want %q", result, "The echo says: hello")
	}
	if wf.Context["s1"] != "The echo says: hello" {
		t.Errorf("context[s1] = %v, want %q", wf.Context["s1"], "The echo says: hello")
	}

	// Verify accumulated usage (20+30=50 prompt, 10+15=25 completion)
	if m.LLMTokensInput.Load() != 50 {
		t.Errorf("LLMTokensInput = %d, want 50", m.LLMTokensInput.Load())
	}
	if m.LLMTokensOutput.Load() != 25 {
		t.Errorf("LLMTokensOutput = %d, want 25", m.LLMTokensOutput.Load())
	}
}

func TestLLMExecutor_ToolCalling_MaxTurnsLimit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always return tool calls — never finishes
		tc := []map[string]any{
			{
				"id":   "call_loop",
				"type": "function",
				"function": map[string]string{
					"name":      "ping",
					"arguments": `{}`,
				},
			},
		}
		_, _ = w.Write(toolCallJSON(tc, 10, 5))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	executor := NewLLMExecutorWithClient(client, NewMetrics())
	executor.SetToolRunner(&mockToolRunner{results: map[string]string{"ping": "pong"}})

	wf := NewWorkflow("wf1", "MaxTurns", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt":    "ping forever",
		"max_turns": float64(3),
		"tools": []any{
			map[string]any{
				"name":        "ping",
				"description": "pings",
			},
		},
	}}

	err := executor.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error for max_turns exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "max_turns") {
		t.Errorf("error = %q, want max_turns mention", err.Error())
	}
}

func TestLLMExecutor_NoTools_NoLoop(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatJSON("direct answer", 10, 5))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	m := NewMetrics()
	executor := NewLLMExecutorWithClient(client, m)
	// No tools configured, no tool runner set

	wf := NewWorkflow("wf1", "NoToolLLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "just answer",
	}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, _ := step.Result.(string)
	if result != "direct answer" {
		t.Errorf("result = %q, want %q", result, "direct answer")
	}
	if m.LLMTokensInput.Load() != 10 {
		t.Errorf("LLMTokensInput = %d, want 10", m.LLMTokensInput.Load())
	}
}
