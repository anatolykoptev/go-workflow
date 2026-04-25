package workflow

import (
	"context"
	"encoding/json"
	"errors"
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

	// Verify tool call log stored in context
	toolCalls, ok := wf.Context["s1_tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls not stored in context: %T", wf.Context["s1_tool_calls"])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls length = %d, want 1", len(toolCalls))
	}
	if toolCalls[0]["name"] != "echo" {
		t.Errorf("tool_calls[0].name = %v, want %q", toolCalls[0]["name"], "echo")
	}
	if toolCalls[0]["result"] != "hello" {
		t.Errorf("tool_calls[0].result = %v, want %q", toolCalls[0]["result"], "hello")
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

// TestLLMExecutor_ToolLoop_RecordsCost verifies that the multi-turn tool
// calling loop records the accumulated cost via recordStepCost (Fix 1).
// Without this, a workflow with WithBudget could blow far past its limit
// inside a single tool-calling step.
func TestLLMExecutor_ToolLoop_RecordsCost(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			tc := []map[string]any{{
				"id":   "call_1",
				"type": "function",
				"function": map[string]string{
					"name":      "echo",
					"arguments": `{"text":"hello"}`,
				},
			}}
			_, _ = w.Write(toolCallJSON(tc, 1000, 500))
			return
		}
		_, _ = w.Write(chatJSON("done", 1000, 500))
	}))
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	eng := NewEngine(store, WithLLMClient(client))
	ex := eng.executors[StepLLM].(*LLMExecutor)
	ex.SetToolRunner(&mockToolRunner{results: map[string]string{"echo": "hello"}})

	wf := NewWorkflow("wf-tlc", "ToolLoopCost", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "echo hello",
		"model":  "claude-haiku-4-5",
		"tools": []any{
			map[string]any{"name": "echo", "description": "echoes"},
		},
	}}
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if wf.Cost == nil {
		t.Fatal("expected Cost recorded, got nil")
	}
	// Two turns: 1000+1000 = 2000 input, 500+500 = 1000 output.
	if wf.Cost.InputTokens != 2000 {
		t.Errorf("InputTokens = %d, want 2000", wf.Cost.InputTokens)
	}
	if wf.Cost.OutputTokens != 1000 {
		t.Errorf("OutputTokens = %d, want 1000", wf.Cost.OutputTokens)
	}
	// haiku: 0.001 * 2 + 0.005 * 1 = 0.007
	want := 0.007
	if !floatNearlyEqual(wf.Cost.USDEstimate, want, 1e-9) {
		t.Errorf("USDEstimate = %f, want %f", wf.Cost.USDEstimate, want)
	}
}

// TestLLMExecutor_ToolLoop_BudgetExceeded verifies that a tool-calling step
// returns ErrBudgetExceeded when accumulated cost crosses the budget.
func TestLLMExecutor_ToolLoop_BudgetExceeded(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			tc := []map[string]any{{
				"id":   "call_1",
				"type": "function",
				"function": map[string]string{"name": "echo", "arguments": `{}`},
			}}
			_, _ = w.Write(toolCallJSON(tc, 100000, 100000))
			return
		}
		_, _ = w.Write(chatJSON("done", 100000, 100000))
	}))
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	eng := NewEngine(store, WithLLMClient(client), WithBudget(0.001))
	ex := eng.executors[StepLLM].(*LLMExecutor)
	ex.SetToolRunner(&mockToolRunner{results: map[string]string{"echo": "x"}})

	wf := NewWorkflow("wf-tlb", "ToolLoopBudget", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "go",
		"model":  "claude-opus-4-7",
		"tools":  []any{map[string]any{"name": "echo"}},
	}}
	err := ex.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected ErrBudgetExceeded, got nil")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("expected ErrBudgetExceeded wrap, got %v", err)
	}
}

// TestLLMExecutor_Stream_RecordsCost verifies that the streaming path
// records cost via recordStepCost (Fix 2).
func TestLLMExecutor_Stream_RecordsCost(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseChunks("hi ", "there")))
	}))
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	eng := NewEngine(store, WithLLMClient(client),
		WithStreamCallback(func(_, _, _ string) {}))
	ex := eng.executors[StepLLM].(*LLMExecutor)

	wf := NewWorkflow("wf-strc", "StreamCost", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "say hi",
		"stream": true,
		"model":  "claude-haiku-4-5",
	}}
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if wf.Cost == nil {
		t.Fatal("expected Cost recorded, got nil")
	}
	// sseChunks final usage: prompt=10, completion=5.
	if wf.Cost.InputTokens != 10 || wf.Cost.OutputTokens != 5 {
		t.Errorf("tokens: in=%d out=%d, want 10/5", wf.Cost.InputTokens, wf.Cost.OutputTokens)
	}
	// haiku: 0.001 * 10/1000 + 0.005 * 5/1000 = 0.00001 + 0.000025 = 0.000035
	want := 0.001*0.01 + 0.005*0.005
	if !floatNearlyEqual(wf.Cost.USDEstimate, want, 1e-9) {
		t.Errorf("USDEstimate = %f, want %f", wf.Cost.USDEstimate, want)
	}
}
