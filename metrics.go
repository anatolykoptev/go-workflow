package workflow

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Metrics tracks workflow execution counters.
type Metrics struct {
	WorkflowsCreated   atomic.Int64
	WorkflowsCompleted atomic.Int64
	WorkflowsFailed    atomic.Int64
	WorkflowsCancelled atomic.Int64
	StepsExecuted      atomic.Int64
	StepsRetried       atomic.Int64
	StepsSkipped       atomic.Int64
	StepsDeadLettered  atomic.Int64
	ApprovalsPending   atomic.Int64
	AgentStepsExecuted atomic.Int64
	AgentStepsFailed   atomic.Int64
	A2AStepsExecuted   atomic.Int64
	A2AStepsFailed     atomic.Int64
	HooksFired         atomic.Int64
	TriggersEvaluated  atomic.Int64
	TriggersFired          atomic.Int64
	SchedulerJobsExecuted  atomic.Int64
	SchedulerJobsFailed    atomic.Int64
	LLMTokensInput         atomic.Int64
	LLMTokensOutput        atomic.Int64
	ImageRendersSuccess    atomic.Int64
	ImageRendersFailed     atomic.Int64
	ImageBytesTotal        atomic.Int64
	ImageDurationMSTotal   atomic.Int64
	VisionCallsSuccess     atomic.Int64
	VisionCallsFailed      atomic.Int64
	VisionTokensInput      atomic.Int64
	VisionTokensOutput     atomic.Int64
	// WorkflowCostUSDTotal stores USD as micro-cents (USD * 1,000,000,
	// rounded) because atomic.Float64 does not exist. Microcents preserve
	// sub-cent precision so cheap-model calls (Haiku/Flash) still move
	// the metric. Divide by 1,000,000 for USD.
	WorkflowCostUSDTotal        atomic.Uint64
	WorkflowTokensInputTotal    atomic.Int64
	WorkflowTokensOutputTotal   atomic.Int64
	WorkflowImagesRenderedTotal atomic.Int64
	WorkflowBudgetExceededTotal atomic.Int64
	WebhooksReceived            atomic.Int64
	WebhooksRejected            atomic.Int64
	StepCacheHits               atomic.Int64
	StepCacheMisses             atomic.Int64
}

// NewMetrics creates a fresh Metrics instance for dependency injection.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// Deprecated: GlobalMetrics is a package-level singleton. Use NewMetrics() + WithMetrics() instead.
var GlobalMetrics = NewMetrics()

// Summary returns a formatted string of all metrics.
func (m *Metrics) Summary() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("  Workflows created: %d", m.WorkflowsCreated.Load()))
	lines = append(lines, fmt.Sprintf("  Workflows completed: %d", m.WorkflowsCompleted.Load()))
	lines = append(lines, fmt.Sprintf("  Workflows failed: %d", m.WorkflowsFailed.Load()))
	lines = append(lines, fmt.Sprintf("  Workflows cancelled: %d", m.WorkflowsCancelled.Load()))
	lines = append(lines, fmt.Sprintf("  Steps executed: %d", m.StepsExecuted.Load()))
	lines = append(lines, fmt.Sprintf("  Steps retried: %d", m.StepsRetried.Load()))
	lines = append(lines, fmt.Sprintf("  Steps skipped: %d", m.StepsSkipped.Load()))
	lines = append(lines, fmt.Sprintf("  Steps dead-lettered: %d", m.StepsDeadLettered.Load()))
	lines = append(lines, fmt.Sprintf("  Approvals pending: %d", m.ApprovalsPending.Load()))
	lines = append(lines, fmt.Sprintf("  Agent steps executed: %d", m.AgentStepsExecuted.Load()))
	lines = append(lines, fmt.Sprintf("  Agent steps failed: %d", m.AgentStepsFailed.Load()))
	lines = append(lines, fmt.Sprintf("  A2A steps executed: %d", m.A2AStepsExecuted.Load()))
	lines = append(lines, fmt.Sprintf("  A2A steps failed: %d", m.A2AStepsFailed.Load()))
	lines = append(lines, fmt.Sprintf("  Hooks fired: %d", m.HooksFired.Load()))
	lines = append(lines, fmt.Sprintf("  Triggers evaluated: %d", m.TriggersEvaluated.Load()))
	lines = append(lines, fmt.Sprintf("  Triggers fired: %d", m.TriggersFired.Load()))
	lines = append(lines, fmt.Sprintf("  Scheduler jobs executed: %d", m.SchedulerJobsExecuted.Load()))
	lines = append(lines, fmt.Sprintf("  Scheduler jobs failed: %d", m.SchedulerJobsFailed.Load()))
	lines = append(lines, fmt.Sprintf("  LLM tokens input: %d", m.LLMTokensInput.Load()))
	lines = append(lines, fmt.Sprintf("  LLM tokens output: %d", m.LLMTokensOutput.Load()))
	lines = append(lines, fmt.Sprintf("  Image renders success: %d", m.ImageRendersSuccess.Load()))
	lines = append(lines, fmt.Sprintf("  Image renders failed: %d", m.ImageRendersFailed.Load()))
	lines = append(lines, fmt.Sprintf("  Image bytes total: %d", m.ImageBytesTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Image duration ms total: %d", m.ImageDurationMSTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Vision calls success: %d", m.VisionCallsSuccess.Load()))
	lines = append(lines, fmt.Sprintf("  Vision calls failed: %d", m.VisionCallsFailed.Load()))
	lines = append(lines, fmt.Sprintf("  Vision tokens input: %d", m.VisionTokensInput.Load()))
	lines = append(lines, fmt.Sprintf("  Vision tokens output: %d", m.VisionTokensOutput.Load()))
	lines = append(lines, fmt.Sprintf("  Workflow cost USD total (microcents): %d", m.WorkflowCostUSDTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Workflow tokens input total: %d", m.WorkflowTokensInputTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Workflow tokens output total: %d", m.WorkflowTokensOutputTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Workflow images rendered total: %d", m.WorkflowImagesRenderedTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Workflow budget exceeded total: %d", m.WorkflowBudgetExceededTotal.Load()))
	lines = append(lines, fmt.Sprintf("  Webhooks received: %d", m.WebhooksReceived.Load()))
	lines = append(lines, fmt.Sprintf("  Webhooks rejected: %d", m.WebhooksRejected.Load()))
	lines = append(lines, fmt.Sprintf("  Step cache hits: %d", m.StepCacheHits.Load()))
	lines = append(lines, fmt.Sprintf("  Step cache misses: %d", m.StepCacheMisses.Load()))
	return strings.Join(lines, "\n")
}

// Reset zeroes all counters.
func (m *Metrics) Reset() {
	m.WorkflowsCreated.Store(0)
	m.WorkflowsCompleted.Store(0)
	m.WorkflowsFailed.Store(0)
	m.WorkflowsCancelled.Store(0)
	m.StepsExecuted.Store(0)
	m.StepsRetried.Store(0)
	m.StepsSkipped.Store(0)
	m.StepsDeadLettered.Store(0)
	m.ApprovalsPending.Store(0)
	m.AgentStepsExecuted.Store(0)
	m.AgentStepsFailed.Store(0)
	m.A2AStepsExecuted.Store(0)
	m.A2AStepsFailed.Store(0)
	m.HooksFired.Store(0)
	m.TriggersEvaluated.Store(0)
	m.TriggersFired.Store(0)
	m.SchedulerJobsExecuted.Store(0)
	m.SchedulerJobsFailed.Store(0)
	m.LLMTokensInput.Store(0)
	m.LLMTokensOutput.Store(0)
	m.ImageRendersSuccess.Store(0)
	m.ImageRendersFailed.Store(0)
	m.ImageBytesTotal.Store(0)
	m.ImageDurationMSTotal.Store(0)
	m.VisionCallsSuccess.Store(0)
	m.VisionCallsFailed.Store(0)
	m.VisionTokensInput.Store(0)
	m.VisionTokensOutput.Store(0)
	m.WorkflowCostUSDTotal.Store(0)
	m.WorkflowTokensInputTotal.Store(0)
	m.WorkflowTokensOutputTotal.Store(0)
	m.WorkflowImagesRenderedTotal.Store(0)
	m.WorkflowBudgetExceededTotal.Store(0)
	m.WebhooksReceived.Store(0)
	m.WebhooksRejected.Store(0)
	m.StepCacheHits.Store(0)
	m.StepCacheMisses.Store(0)
}
