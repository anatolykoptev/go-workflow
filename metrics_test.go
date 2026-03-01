package workflow

import (
	"strings"
	"testing"
)

func TestMetrics_Summary(t *testing.T) {
	GlobalMetrics.Reset()
	GlobalMetrics.AgentStepsExecuted.Store(5)
	GlobalMetrics.HooksFired.Store(12)

	summary := GlobalMetrics.Summary()
	if !strings.Contains(summary, "Agent steps executed: 5") {
		t.Errorf("summary missing agent steps: %s", summary)
	}
	if !strings.Contains(summary, "Hooks fired: 12") {
		t.Errorf("summary missing hooks fired: %s", summary)
	}
}

func TestMetrics_Reset(t *testing.T) {
	GlobalMetrics.AgentStepsExecuted.Store(10)
	GlobalMetrics.AgentStepsFailed.Store(3)
	GlobalMetrics.HooksFired.Store(7)
	GlobalMetrics.Reset()

	if GlobalMetrics.AgentStepsExecuted.Load() != 0 {
		t.Error("AgentStepsExecuted not reset")
	}
	if GlobalMetrics.AgentStepsFailed.Load() != 0 {
		t.Error("AgentStepsFailed not reset")
	}
	if GlobalMetrics.HooksFired.Load() != 0 {
		t.Error("HooksFired not reset")
	}
}
