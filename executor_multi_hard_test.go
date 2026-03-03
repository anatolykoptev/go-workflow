package workflow

import (
	"context"
	"errors"
	"testing"
)

// errorRunner always returns an error.
type errorRunner struct {
	err error
}

func (r *errorRunner) Execute(_ context.Context, _ string, _ map[string]any) (string, error) {
	return "", r.err
}

// TestMultiToolRunner_FallbackSwallowsAll verifies that a fallback runner
// registered first intercepts all calls, even if a specific runner is added later.
func TestMultiToolRunner_FallbackSwallowsAll(t *testing.T) {
	fallback := &stubRunner{response: "fallback"}
	specific := &stubRunner{response: "specific"}

	// Fallback first, then specific — fallback should win for everything.
	multi := NewMultiToolRunner(fallback)
	multi.Register(specific, "my_tool")

	result, err := multi.Execute(context.Background(), "my_tool", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// BUG DETECTOR: if specific wins, ordering semantics are broken.
	if result != "fallback" {
		t.Errorf("got %q, want %q — fallback should win when registered first", result, "fallback")
	}
	if !fallback.called {
		t.Error("expected fallback to be called")
	}
	if specific.called {
		t.Error("expected specific to NOT be called")
	}
}

// TestMultiToolRunner_ErrorDoesNotFallThrough verifies that when a matched
// runner returns an error, Execute returns that error immediately without
// trying the next runner.
func TestMultiToolRunner_ErrorDoesNotFallThrough(t *testing.T) {
	broken := &errorRunner{err: errors.New("broken")}
	backup := &stubRunner{response: "backup"}

	multi := &MultiToolRunner{}
	multi.Register(broken, "tool_x")
	multi.Register(backup) // fallback

	_, err := multi.Execute(context.Background(), "tool_x", nil)
	if err == nil {
		t.Fatal("expected error from broken runner")
	}
	if err.Error() != "broken" {
		t.Errorf("got %q, want %q", err.Error(), "broken")
	}
	if backup.called {
		t.Error("backup runner should NOT be called when specific runner errors")
	}
}

// TestMultiToolRunner_EmptyRunners verifies error with no runners.
func TestMultiToolRunner_EmptyRunners(t *testing.T) {
	multi := &MultiToolRunner{}
	_, err := multi.Execute(context.Background(), "anything", nil)
	if err == nil {
		t.Fatal("expected error with empty runner list")
	}
}

// TestMultiToolRunner_SpecificBeforeFallback verifies correct routing
// when specific runner is registered before fallback.
func TestMultiToolRunner_SpecificBeforeFallback(t *testing.T) {
	specific := &stubRunner{response: "specific"}
	fallback := &stubRunner{response: "fallback"}

	multi := &MultiToolRunner{}
	multi.Register(specific, "my_tool")
	multi.Register(fallback) // fallback

	// Specific tool → specific runner.
	result, err := multi.Execute(context.Background(), "my_tool", nil)
	if err != nil {
		t.Fatalf("execute my_tool: %v", err)
	}
	if result != "specific" {
		t.Errorf("got %q, want %q", result, "specific")
	}

	// Unknown tool → fallback runner.
	specific.called = false
	result, err = multi.Execute(context.Background(), "other", nil)
	if err != nil {
		t.Fatalf("execute other: %v", err)
	}
	if result != "fallback" {
		t.Errorf("got %q, want %q", result, "fallback")
	}
	if specific.called {
		t.Error("specific should not be called for unknown tool")
	}
}

// TestMultiToolRunner_MultipleFallbacks verifies first fallback wins.
func TestMultiToolRunner_MultipleFallbacks(t *testing.T) {
	first := &stubRunner{response: "first"}
	second := &stubRunner{response: "second"}

	multi := NewMultiToolRunner(first, second)

	result, err := multi.Execute(context.Background(), "anything", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result != "first" {
		t.Errorf("got %q, want %q — first fallback should win", result, "first")
	}
	if second.called {
		t.Error("second fallback should NOT be called")
	}
}

// TestMultiToolRunner_NewMultiToolRunnerAsFallbacks verifies that
// NewMultiToolRunner registers all runners as fallbacks (tools=nil).
func TestMultiToolRunner_NewMultiToolRunnerAsFallbacks(t *testing.T) {
	a := &stubRunner{response: "a"}

	multi := NewMultiToolRunner(a)

	// Any tool should route to the fallback.
	result, err := multi.Execute(context.Background(), "random_tool_name", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result != "a" {
		t.Errorf("got %q, want %q", result, "a")
	}
}
