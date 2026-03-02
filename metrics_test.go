package workflow

import (
	"strings"
	"testing"
)

func TestMetrics_Summary(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.AgentStepsExecuted.Store(5)
	m.HooksFired.Store(12)

	summary := m.Summary()
	if !strings.Contains(summary, "Agent steps executed: 5") {
		t.Errorf("summary missing agent steps: %s", summary)
	}
	if !strings.Contains(summary, "Hooks fired: 12") {
		t.Errorf("summary missing hooks fired: %s", summary)
	}
}

func TestMetrics_Reset(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.AgentStepsExecuted.Store(10)
	m.AgentStepsFailed.Store(3)
	m.HooksFired.Store(7)
	m.Reset()

	if m.AgentStepsExecuted.Load() != 0 {
		t.Error("AgentStepsExecuted not reset")
	}
	if m.AgentStepsFailed.Load() != 0 {
		t.Error("AgentStepsFailed not reset")
	}
	if m.HooksFired.Load() != 0 {
		t.Error("HooksFired not reset")
	}
}
