package workflow

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// tracerName is the instrumentation library name reported on every span. OTel
// backends use this to group spans by the library that emitted them.
const tracerName = "github.com/anatolykoptev/go-workflow"

// errKindBudget / errKindExecutor are the stable labels applied to
// span.error.kind. Bounded vocabulary keeps trace-backend cardinality low.
const (
	errKindBudget   = "budget_exceeded"
	errKindExecutor = "executor_error"
)

// WithTracerProvider opts the engine into OpenTelemetry tracing. Every
// workflow gets a parent span; every step gets a child span with cost,
// duration, and outcome attributes. When unset, all tracing helpers no-op
// and observable behaviour is identical to v0.10.x.
//
// Operators wire any OTel-compatible exporter (Jaeger, Tempo, Honeycomb,
// Datadog, OTLP) via the standard SDK and pass the resulting
// trace.TracerProvider here.
func WithTracerProvider(tp trace.TracerProvider) EngineOption {
	return func(e *Engine) {
		if tp == nil {
			return
		}
		e.tracerProvider = tp
		e.tracer = tp.Tracer(tracerName)
	}
}

// tracerOrNoop returns the engine's tracer or a noop implementation. The
// no-op tracer returns spans whose IsRecording() is false so attribute
// setting and end-of-span work compile to nearly nothing.
func (e *Engine) tracerOrNoop() trace.Tracer {
	if e.tracer != nil {
		return e.tracer
	}
	return noop.NewTracerProvider().Tracer(tracerName)
}

// startWorkflowSpan opens a span representing one workflow execution. The
// caller MUST call span.End() (or use the returned context's eventual
// cancellation pattern). Returns the child context so downstream
// operations propagate the trace.
func (e *Engine) startWorkflowSpan(ctx context.Context, wf *Workflow) (context.Context, trace.Span) {
	if wf == nil {
		return noop.NewTracerProvider().Tracer(tracerName).Start(ctx, "workflow.run")
	}
	attrs := []attribute.KeyValue{
		attribute.String("workflow.id", wf.ID),
		attribute.String("workflow.name", wf.Name),
	}
	if wf.TemplateName != "" {
		attrs = append(attrs, attribute.String("workflow.template", wf.TemplateName))
	}
	if wf.Owner != "" {
		attrs = append(attrs, attribute.String("workflow.owner", wf.Owner))
	}
	return e.tracerOrNoop().Start(ctx, "workflow.run", trace.WithAttributes(attrs...))
}

// startStepSpan opens a child span for a single step execution. Pre-populates
// step.id and step.kind; cost / duration attributes are added by
// finishStepSpan once the executor returns.
func (e *Engine) startStepSpan(ctx context.Context, wf *Workflow, step *Step) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("step.id", step.ID),
		attribute.String("step.kind", string(step.Kind)),
	}
	if wf != nil {
		attrs = append(attrs, attribute.String("workflow.id", wf.ID))
	}
	// Surface the configured model when present — useful filter in trace
	// backends ("show me all gpt-4 spans last hour").
	if model, ok := step.Config["model"].(string); ok && model != "" {
		attrs = append(attrs, attribute.String("step.model", model))
	}
	return e.tracerOrNoop().Start(ctx, "step."+string(step.Kind), trace.WithAttributes(attrs...))
}

// finishStepSpan records cost, duration, cache, and error attributes on the
// step span and ends it. Safe to call with a noop span (all calls are
// no-ops in that case).
func finishStepSpan(span trace.Span, step *Step, durationMS int64, cacheHit bool, execErr error) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.Int64("step.duration_ms", durationMS),
		attribute.Bool("step.cache_hit", cacheHit),
	)
	// Cost may be zero for non-cost-bearing steps; report what's there.
	if cost := stepCostFromResult(step); cost != nil {
		span.SetAttributes(
			attribute.Int64("step.input_tokens", cost.InputTokens),
			attribute.Int64("step.output_tokens", cost.OutputTokens),
			attribute.Float64("step.usd_estimate", cost.USDEstimate),
		)
		if cost.Model != "" {
			span.SetAttributes(attribute.String("step.model", cost.Model))
		}
	}
	if execErr != nil {
		span.SetAttributes(
			attribute.String("step.error.kind", classifyErrorKind(execErr)),
			attribute.String("step.error.message", execErr.Error()),
		)
		span.SetStatus(codes.Error, execErr.Error())
	}
	span.End()
}

// stepCostFromResult extracts a per-step cost record from the step result
// payload. Executors that record cost (LLM, Vision, Image) deposit the
// StepCost into wf.Cost.BySteps via recordStepCost; this helper looks it up
// for the trace span without re-computing.
func stepCostFromResult(step *Step) *StepCost {
	if step == nil || step.Result == nil {
		return nil
	}
	if m, ok := step.Result.(map[string]any); ok {
		if c, ok := m["_cost"].(StepCost); ok {
			return &c
		}
	}
	return nil
}

// classifyErrorKind buckets an executor error into a stable label for
// span.error.kind. Keeps the cardinality bounded for trace-backend filters.
func classifyErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case isBudgetError(err):
		return errKindBudget
	default:
		return errKindExecutor
	}
}

// workflowSpanFinalAttrs returns the closing attributes added to the
// workflow span just before it ends (state + cost summary).
func workflowSpanFinalAttrs(wf *Workflow) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("workflow.state", string(wf.State)),
		attribute.Int("workflow.steps_executed", wf.StepsExecuted),
	}
	if wf.Cost != nil {
		attrs = append(attrs,
			attribute.Int64("workflow.input_tokens", wf.Cost.InputTokens),
			attribute.Int64("workflow.output_tokens", wf.Cost.OutputTokens),
			attribute.Float64("workflow.usd_estimate", wf.Cost.USDEstimate),
		)
	}
	return attrs
}

func isBudgetError(err error) bool {
	for e := err; e != nil; {
		if errors.Is(e, ErrBudgetExceeded) {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
