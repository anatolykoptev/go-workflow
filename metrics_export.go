package workflow

import (
	"fmt"
	"net/http"
	"strings"
)

// PrometheusHandler returns an http.Handler that renders GlobalMetrics
// in Prometheus text exposition format. No external dependency required.
func PrometheusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		m := GlobalMetrics
		var b strings.Builder

		gauge := func(name, help string, val int64) {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
			fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
			fmt.Fprintf(&b, "%s %d\n", name, val)
		}

		counter := func(name, help string, val int64) {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
			fmt.Fprintf(&b, "# TYPE %s counter\n", name)
			fmt.Fprintf(&b, "%s %d\n", name, val)
		}

		counter("workflow_workflows_created_total", "Total workflows created", m.WorkflowsCreated.Load())
		counter("workflow_workflows_completed_total", "Total workflows completed", m.WorkflowsCompleted.Load())
		counter("workflow_workflows_failed_total", "Total workflows failed", m.WorkflowsFailed.Load())
		counter("workflow_workflows_cancelled_total", "Total workflows cancelled", m.WorkflowsCancelled.Load())
		counter("workflow_steps_executed_total", "Total steps executed", m.StepsExecuted.Load())
		counter("workflow_steps_retried_total", "Total step retries", m.StepsRetried.Load())
		counter("workflow_steps_skipped_total", "Total steps skipped", m.StepsSkipped.Load())
		counter("workflow_steps_dead_lettered_total", "Total steps dead-lettered", m.StepsDeadLettered.Load())
		gauge("workflow_approvals_pending", "Current pending approvals", m.ApprovalsPending.Load())
		counter("workflow_agent_steps_executed_total", "Total agent steps executed", m.AgentStepsExecuted.Load())
		counter("workflow_agent_steps_failed_total", "Total agent steps failed", m.AgentStepsFailed.Load())
		counter("workflow_a2a_steps_executed_total", "Total A2A steps executed", m.A2AStepsExecuted.Load())
		counter("workflow_a2a_steps_failed_total", "Total A2A steps failed", m.A2AStepsFailed.Load())
		counter("workflow_hooks_fired_total", "Total hook events fired", m.HooksFired.Load())
		counter("workflow_triggers_evaluated_total", "Total trigger evaluations", m.TriggersEvaluated.Load())
		counter("workflow_triggers_fired_total", "Total triggers fired", m.TriggersFired.Load())
		counter("workflow_scheduler_jobs_executed_total", "Total scheduler jobs executed", m.SchedulerJobsExecuted.Load())
		counter("workflow_scheduler_jobs_failed_total", "Total scheduler jobs failed", m.SchedulerJobsFailed.Load())
		counter("workflow_llm_tokens_input_total", "Total LLM input tokens", m.LLMTokensInput.Load())
		counter("workflow_llm_tokens_output_total", "Total LLM output tokens", m.LLMTokensOutput.Load())

		_, _ = w.Write([]byte(b.String()))
	})
}
