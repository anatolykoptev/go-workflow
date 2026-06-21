package workflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/llm"
)

// TestToolLoop_Breaker_TripsOnToolFailures verifies that the circuit breaker
// wrapping toolRunner.Execute in executeTool fails fast after FailThreshold
// consecutive failures — the runner is not called beyond the threshold.
//
// Red-on-revert: remove the e.breakers.call("tool:"+tc.Function.Name, ...) guard
// in executeTool and this test fails — runner call count reaches max_turns (10)
// instead of stopping at the trip threshold (3), proving the guard is load-bearing.
func TestToolLoop_Breaker_TripsOnToolFailures(t *testing.T) {
	t.Parallel()

	const failThreshold = 3

	var runnerCalls atomic.Int32
	runner := &countingFailRunner{calls: &runnerCalls}

	// The LLM always returns a single tool call — it never produces a final
	// content response — so without the breaker, the tool-loop would invoke
	// the runner on every turn up to max_turns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(toolCallJSON(tc("c1", "dead-tool", `{}`), 5, 3))
	}))
	t.Cleanup(srv.Close)

	// Inject a real breakerRegistry with a tight threshold so the test is fast.
	reg := &breakerRegistry{}
	b := freshBreaker("tool:dead-tool", failThreshold, 10*time.Second)
	reg.m.Store("tool:dead-tool", b)

	c := llm.NewClient(srv.URL, "k", "m", llm.WithMaxRetries(1))
	ex := NewLLMExecutorWithClient(c, NewMetrics())
	ex.SetToolRunner(runner)
	ex.breakers = reg // inject the tight-threshold registry

	wf, step := toolStep("s1", "go", []any{td("dead-tool")}, map[string]any{"max_turns": float64(10)})
	_ = ex.Execute(context.Background(), step, wf)

	gotCalls := runnerCalls.Load()

	// After failThreshold failures the breaker opens; every subsequent turn
	// returns circuitOpenError without invoking the runner.
	// runner call count must equal exactly failThreshold, not max_turns (10).
	if gotCalls != failThreshold {
		t.Errorf(
			"runner called %d times; want exactly %d (the trip threshold) — "+
				"subsequent turns must short-circuit via the breaker, not call the runner",
			gotCalls, failThreshold,
		)
	}
}

// TestToolLoop_Breaker_SharedRegistry verifies that a tool tripped via
// ToolExecutor (StepTool) also shows as open in the LLM tool-loop, because
// both wires use the same engine-scoped breakerRegistry instance.
//
// Red-on-revert: if the LLM tool-loop used a separate registry (or no registry),
// the pre-opened breaker would be invisible to it and the runner would be called
// during the LLM turns, failing the assertion that afterCalls == beforeCalls.
func TestToolLoop_Breaker_SharedRegistry(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)

	const failThreshold = 3

	var runnerCalls atomic.Int32
	runner := &countingFailRunner{calls: &runnerCalls}

	// LLM server always returns a tool call for "shared-tool".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(toolCallJSON(tc("c1", "shared-tool", `{}`), 5, 3))
	}))
	t.Cleanup(srv.Close)

	c := llm.NewClient(srv.URL, "k", "m", llm.WithMaxRetries(1))
	eng := NewEngine(store, WithLLMClient(c), WithToolRunner(runner))

	// Pre-seed the engine's registry with a tight-threshold breaker for
	// "tool:shared-tool" so we control when it trips without needing 5 failures.
	b := freshBreaker("tool:shared-tool", failThreshold, 10*time.Second)
	eng.breakers.m.Store("tool:shared-tool", b)

	// Trip the breaker via ToolExecutor (failThreshold direct failures).
	toolEx := eng.executors[StepTool].(*ToolExecutor)
	for range failThreshold {
		s := &Step{ID: "trip", Kind: StepTool, Config: map[string]any{"tool": "shared-tool"}}
		_ = toolEx.Execute(context.Background(), s, NewWorkflow("w", "T", "t:1", nil))
	}

	// Now use the same Engine's LLM executor to invoke the same tool via the
	// tool-loop. The breaker should already be open — runner must NOT be called.
	llmEx := eng.executors[StepLLM].(*LLMExecutor)
	beforeCalls := runnerCalls.Load()

	wf, step := toolStep("s2", "go", []any{td("shared-tool")}, map[string]any{"max_turns": float64(3)})
	_ = llmEx.Execute(context.Background(), step, wf)

	if runnerCalls.Load() != beforeCalls {
		t.Errorf(
			"runner called %d time(s) after breaker was already open via ToolExecutor; want 0 — "+
				"both executor paths must share the same breakerRegistry",
			runnerCalls.Load()-beforeCalls,
		)
	}
}

// countingFailRunner records every Execute invocation and always returns an error.
type countingFailRunner struct {
	calls *atomic.Int32
}

func (r *countingFailRunner) Execute(_ context.Context, _ string, _ map[string]any) (string, error) {
	r.calls.Add(1)
	return "", errors.New("tool endpoint unreachable")
}
