package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTracingEngine(t *testing.T, runner ToolRunner) (*Engine, *WorkflowStore, *tracetest.SpanRecorder) {
	t.Helper()
	engine, store := newTestEngine(t, runner)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	WithTracerProvider(tp)(engine)
	return engine, store, rec
}

func TestTracing_WorkflowAndStepSpans(t *testing.T) {
	t.Parallel()

	engine, store, rec := newTracingEngine(t, &mockToolRunner{})

	wf := NewWorkflow("wf-trace", "Trace", "owner:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
		{ID: "b", Kind: StepTool, Config: map[string]any{"tool": "tb"}, DependsOn: []string{"a"}, State: StepPending},
	})
	if err := store.Save(wf); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := engine.Start(context.Background(), "wf-trace"); err != nil {
		t.Fatalf("start: %v", err)
	}

	spans := rec.Ended()
	var workflowSpans, stepSpans int
	for _, s := range spans {
		switch s.Name() {
		case "workflow.run":
			workflowSpans++
		case "step.tool":
			stepSpans++
		}
	}
	if workflowSpans != 1 {
		t.Errorf("expected 1 workflow span, got %d", workflowSpans)
	}
	if stepSpans != 2 {
		t.Errorf("expected 2 step spans, got %d", stepSpans)
	}

	// Verify step span attributes are populated.
	for _, s := range spans {
		if s.Name() != "step.tool" {
			continue
		}
		var sawID, sawKind, sawDuration bool
		for _, a := range s.Attributes() {
			switch string(a.Key) {
			case "step.id":
				sawID = true
			case "step.kind":
				sawKind = a.Value.AsString() == "tool"
			case "step.duration_ms":
				sawDuration = true
			}
		}
		if !sawID || !sawKind || !sawDuration {
			t.Errorf("step span missing required attrs: id=%v kind=%v dur=%v", sawID, sawKind, sawDuration)
		}
	}
}

func TestTracing_ErrorMarksSpanError(t *testing.T) {
	t.Parallel()

	runner := &selectiveToolRunner{failTools: map[string]bool{"bad": true}}
	engine, store, rec := newTracingEngine(t, runner)

	wf := NewWorkflow("wf-trace-err", "Err", "owner:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "bad"}, State: StepPending},
	})
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), "wf-trace-err")

	var found bool
	for _, s := range rec.Ended() {
		if s.Name() != "step.tool" {
			continue
		}
		if s.Status().Code.String() != "Error" {
			continue
		}
		var hasKind, hasMsg bool
		for _, a := range s.Attributes() {
			switch string(a.Key) {
			case "step.error.kind":
				hasKind = a.Value.AsString() == "executor_error"
			case "step.error.message":
				hasMsg = strings.Contains(a.Value.AsString(), "tool bad failed")
			}
		}
		if hasKind && hasMsg {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error step span with classified kind + message")
	}
}

func TestTracing_NoProviderIsNoOp(t *testing.T) {
	t.Parallel()

	// Without WithTracerProvider, engine must execute identically and produce
	// no spans (we cannot observe anything, so just verify nothing panics
	// and the workflow completes).
	engine, store := newTestEngine(t, &mockToolRunner{})

	wf := NewWorkflow("wf-no-trace", "NoTrace", "owner:1", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "ta"}, State: StepPending},
	})
	_ = store.Save(wf)
	if err := engine.Start(context.Background(), "wf-no-trace"); err != nil {
		t.Fatalf("start: %v", err)
	}
	loaded, _ := store.Load("wf-no-trace")
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}

func TestTracing_BudgetErrorClassified(t *testing.T) {
	t.Parallel()

	// classifyErrorKind unit test — make sure ErrBudgetExceeded is bucketed
	// into "budget_exceeded" not the catch-all.
	if got := classifyErrorKind(ErrBudgetExceeded); got != "budget_exceeded" {
		t.Errorf("classifyErrorKind(ErrBudgetExceeded) = %q, want budget_exceeded", got)
	}
	wrapped := errors.New("other")
	if got := classifyErrorKind(wrapped); got != "executor_error" {
		t.Errorf("classifyErrorKind(other) = %q, want executor_error", got)
	}
	if got := classifyErrorKind(nil); got != "" {
		t.Errorf("classifyErrorKind(nil) = %q, want empty", got)
	}
}
