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
}
