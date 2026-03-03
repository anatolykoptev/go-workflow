package workflow

import (
	"context"
	"fmt"
	"testing"
)

// stubRunner is a test ToolRunner that returns a fixed response.
type stubRunner struct {
	response string
	called   bool
}

func (s *stubRunner) Execute(_ context.Context, _ string, _ map[string]any) (string, error) {
	s.called = true
	return s.response, nil
}

func TestMultiToolRunner_RoutesToCorrectRunner(t *testing.T) {
	runnerA := &stubRunner{response: "from A"}
	runnerB := &stubRunner{response: "from B"}

	multi := &MultiToolRunner{}
	multi.Register(runnerA, "tool_a")
	multi.Register(runnerB, "tool_b")

	result, err := multi.Execute(context.Background(), "tool_a", nil)
	if err != nil {
		t.Fatalf("execute tool_a: %v", err)
	}
	if result != "from A" {
		t.Errorf("got %q, want %q", result, "from A")
	}
	if !runnerA.called {
		t.Error("expected runnerA to be called")
	}
	if runnerB.called {
		t.Error("expected runnerB to NOT be called")
	}
}

func TestMultiToolRunner_FallbackRunner(t *testing.T) {
	specific := &stubRunner{response: "specific"}
	fallback := &stubRunner{response: "fallback"}

	multi := &MultiToolRunner{}
	multi.Register(specific, "known_tool")
	multi.Register(fallback) // no tool names = fallback

	// Known tool goes to specific runner.
	result, err := multi.Execute(context.Background(), "known_tool", nil)
	if err != nil {
		t.Fatalf("execute known_tool: %v", err)
	}
	if result != "specific" {
		t.Errorf("got %q, want %q", result, "specific")
	}

	// Unknown tool falls through to fallback.
	specific.called = false
	result, err = multi.Execute(context.Background(), "unknown_tool", nil)
	if err != nil {
		t.Fatalf("execute unknown_tool: %v", err)
	}
	if result != "fallback" {
		t.Errorf("got %q, want %q", result, "fallback")
	}
	if specific.called {
		t.Error("expected specific runner to NOT be called for unknown tool")
	}
}

func TestMultiToolRunner_NoMatch(t *testing.T) {
	specific := &stubRunner{response: "only mine"}

	multi := &MultiToolRunner{}
	multi.Register(specific, "my_tool")

	_, err := multi.Execute(context.Background(), "other_tool", nil)
	if err == nil {
		t.Fatal("expected error for unmatched tool")
	}

	expected := fmt.Sprintf("tool %q: no runner registered", "other_tool")
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}
