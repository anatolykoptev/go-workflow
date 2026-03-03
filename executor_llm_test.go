package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anatolykoptev/go-kit/llm"
)

// chatJSON returns an OpenAI-compatible chat completion JSON response.
func chatJSON(content string, promptTok, completionTok int) []byte {
	resp := map[string]any{
		"choices": []map[string]any{
			{
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": "stop",
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

// sseChunks builds Server-Sent Events payload for streaming tests.
func sseChunks(deltas ...string) string {
	var buf strings.Builder
	for _, d := range deltas {
		chunk := map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]string{"content": d}, "finish_reason": ""},
			},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(&buf, "data: %s\n\n", b)
	}
	// Final usage chunk
	usage := map[string]any{
		"choices": []map[string]any{},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
	b, _ := json.Marshal(usage)
	fmt.Fprintf(&buf, "data: %s\n\n", b)
	buf.WriteString("data: [DONE]\n\n")
	return buf.String()
}

func TestLLMExecutor_WithProvider(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	provider := &stressLLM{response: &LLMResponse{
		Content: "hello world", InputTokens: 10, OutputTokens: 5, Model: "test-model",
	}}
	executor := NewLLMExecutor(provider, m)

	wf := NewWorkflow("wf1", "LLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "say hello"}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step.Result != "hello world" {
		t.Errorf("result = %q, want %q", step.Result, "hello world")
	}
	if wf.Context["s1"] != "hello world" {
		t.Errorf("context[s1] = %v, want %q", wf.Context["s1"], "hello world")
	}
	if m.LLMTokensInput.Load() != 10 {
		t.Errorf("LLMTokensInput = %d, want 10", m.LLMTokensInput.Load())
	}
	if m.LLMTokensOutput.Load() != 5 {
		t.Errorf("LLMTokensOutput = %d, want 5", m.LLMTokensOutput.Load())
	}
}

func TestLLMExecutor_WithClient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatJSON("hello from client", 10, 5))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	m := NewMetrics()
	executor := NewLLMExecutorWithClient(client, m)

	wf := NewWorkflow("wf1", "LLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "say hello"}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step.Result != "hello from client" {
		t.Errorf("result = %q, want %q", step.Result, "hello from client")
	}
	if wf.Context["s1"] != "hello from client" {
		t.Errorf("context[s1] = %v, want %q", wf.Context["s1"], "hello from client")
	}
	if m.LLMTokensInput.Load() != 10 {
		t.Errorf("LLMTokensInput = %d, want 10", m.LLMTokensInput.Load())
	}
}

func TestLLMExecutor_StreamCallbackFired(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseChunks("hel", "lo ", "world")))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	m := NewMetrics()
	executor := NewLLMExecutorWithClient(client, m)

	var mu sync.Mutex
	var deltas []string
	executor.SetStreamCallback(func(wfID, stepID, delta string) {
		mu.Lock()
		deltas = append(deltas, delta)
		mu.Unlock()
	})

	wf := NewWorkflow("wf1", "StreamLLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "say hello",
		"stream": true,
	}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step.Result != "hello world" {
		t.Errorf("result = %q, want %q", step.Result, "hello world")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != 3 {
		t.Fatalf("got %d deltas, want 3: %v", len(deltas), deltas)
	}
	if got := strings.Join(deltas, ""); got != "hello world" {
		t.Errorf("joined deltas = %q, want %q", got, "hello world")
	}
}

func TestLLMExecutor_StreamFallbackWithoutCallback(t *testing.T) {
	t.Parallel()
	// When stream=true but no callback, should use non-streaming Chat path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatJSON("non-stream", 5, 3))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	executor := NewLLMExecutorWithClient(client, NewMetrics())

	wf := NewWorkflow("wf1", "FallbackLLM", "telegram:1", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"prompt": "say hello",
		"stream": true,
	}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step.Result != "non-stream" {
		t.Errorf("result = %q, want %q", step.Result, "non-stream")
	}
}

func TestLLMExecutor_SkillResolution(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		msgs := req["messages"].([]any)
		lastMsg := msgs[len(msgs)-1].(map[string]any)
		content := lastMsg["content"].(string)

		w.Header().Set("Content-Type", "application/json")
		// Echo back the prompt for verification
		_, _ = w.Write(chatJSON("echo:"+content, 5, 3))
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient(srv.URL, "test-key", "test-model", llm.WithMaxRetries(1))
	executor := NewLLMExecutorWithClient(client, NewMetrics())
	executor.SetSkills(&mockSkillResolver{skills: map[string]string{
		"research": "You are a researcher.",
	}})

	wf := NewWorkflow("wf1", "SkillClient", "telegram:1", nil)
	wf.Context["data"] = "raw data"
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{
		"skill": "research",
		"input": "Analyze: {{data}}",
	}}

	if err := executor.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result, _ := step.Result.(string)
	if !strings.Contains(result, "researcher") {
		t.Errorf("result = %q, want skill prompt in echo", result)
	}
	if !strings.Contains(result, "raw data") {
		t.Errorf("result = %q, want resolved input in echo", result)
	}
}
