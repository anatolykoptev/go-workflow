package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/llm"
)

// --- test helpers for hard tests ---

func newToolExec(t *testing.T, url string, runner ToolRunner) *LLMExecutor {
	t.Helper()
	c := llm.NewClient(url, "k", "m", llm.WithMaxRetries(1))
	ex := NewLLMExecutorWithClient(c, NewMetrics())
	ex.SetToolRunner(runner)
	return ex
}

func toolStep(id, prompt string, tools []any, extra ...map[string]any) (*Workflow, *Step) {
	cfg := map[string]any{"prompt": prompt, "tools": tools}
	for _, m := range extra {
		for k, v := range m {
			cfg[k] = v
		}
	}
	return NewWorkflow("wf1", "T", "t:1", nil), &Step{ID: id, Kind: StepLLM, Config: cfg}
}

func td(name string) map[string]any { return map[string]any{"name": name, "description": name} }

func tc(id, name, args string) []map[string]any {
	return []map[string]any{{
		"id": id, "type": "function",
		"function": map[string]string{"name": name, "arguments": args},
	}}
}

// argsCapture captures the args passed to a tool for assertion.
type argsCapture struct {
	captured *map[string]any
	result   string
}

func (a *argsCapture) Execute(_ context.Context, _ string, args map[string]any) (string, error) {
	*a.captured = args
	return a.result, nil
}

// --- hard loop tests ---

func TestToolLoop_Hard_ErrorForwardedToLLM(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			_, _ = w.Write(toolCallJSON(tc("c1", "boom", `{}`), 10, 5))
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		msgs := req["messages"].([]any)
		toolMsg := msgs[len(msgs)-1].(map[string]any)
		if c, _ := toolMsg["content"].(string); !strings.HasPrefix(c, "error:") {
			t.Errorf("error not forwarded to LLM, got %q", c)
		}
		_, _ = w.Write(chatJSON("handled", 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, &selectiveToolRunner{failTools: map[string]bool{"boom": true}})
	wf, step := toolStep("s1", "go", []any{td("boom")})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	log := wf.Context["s1_tool_calls"].([]map[string]any)
	if !strings.HasPrefix(log[0]["result"].(string), "error:") {
		t.Errorf("tool log result = %q, want error: prefix", log[0]["result"])
	}
}

func TestToolLoop_Hard_SecurityBlocksOneOfTwoTools(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			calls := []map[string]any{
				{"id": "c1", "type": "function", "function": map[string]string{"name": "safe", "arguments": `{}`}},
				{"id": "c2", "type": "function", "function": map[string]string{"name": "banned", "arguments": `{}`}},
			}
			_, _ = w.Write(toolCallJSON(calls, 10, 5))
			return
		}
		_, _ = w.Write(chatJSON("done", 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, &mockToolRunner{results: map[string]string{"safe": "ok"}})
	wf, step := toolStep("s1", "go", []any{td("safe"), td("banned")})
	wf.Security = &SecurityPolicy{DeniedTools: []string{"banned"}}

	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	log := wf.Context["s1_tool_calls"].([]map[string]any)
	if len(log) != 2 {
		t.Fatalf("tool log length = %d, want 2", len(log))
	}
	if log[0]["result"] != "ok" {
		t.Errorf("safe tool result = %v, want ok", log[0]["result"])
	}
	if !strings.Contains(log[1]["result"].(string), "not permitted") {
		t.Errorf("banned tool result = %v, want 'not permitted'", log[1]["result"])
	}
}

func TestToolLoop_Hard_MultipleToolCallsPerTurn(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			calls := []map[string]any{
				{"id": "c1", "type": "function", "function": map[string]string{"name": "a", "arguments": `{}`}},
				{"id": "c2", "type": "function", "function": map[string]string{"name": "b", "arguments": `{}`}},
				{"id": "c3", "type": "function", "function": map[string]string{"name": "c", "arguments": `{}`}},
			}
			_, _ = w.Write(toolCallJSON(calls, 10, 5))
			return
		}
		_, _ = w.Write(chatJSON("all done", 10, 5))
	}))
	t.Cleanup(srv.Close)

	runner := &mockToolRunner{results: map[string]string{"a": "r1", "b": "r2", "c": "r3"}}
	ex := newToolExec(t, srv.URL, runner)
	wf, step := toolStep("s1", "go", []any{td("a"), td("b"), td("c")})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	log := wf.Context["s1_tool_calls"].([]map[string]any)
	if len(log) != 3 {
		t.Fatalf("tool log length = %d, want 3", len(log))
	}
	for i, name := range []string{"a", "b", "c"} {
		if log[i]["name"] != name {
			t.Errorf("log[%d].name = %v, want %q", i, log[i]["name"], name)
		}
	}
}

func TestToolLoop_Hard_ContextCancellation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(toolCallJSON(tc("c1", "slow", `{}`), 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, &slowToolRunner{delay: 5 * time.Second})
	wf, step := toolStep("s1", "go", []any{td("slow")})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := ex.Execute(ctx, step, wf)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("error = %q, want context-related", err.Error())
	}
}
