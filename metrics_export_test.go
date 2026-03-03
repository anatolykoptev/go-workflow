package workflow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusHandler(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.WorkflowsCreated.Add(5)
	m.StepsExecuted.Add(10)
	m.LLMTokensInput.Add(1000)
	m.LLMTokensOutput.Add(500)

	handler := PrometheusHandler(m)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	checks := []string{
		"workflow_workflows_created_total 5",
		"workflow_steps_executed_total 10",
		"workflow_llm_tokens_input_total 1000",
		"workflow_llm_tokens_output_total 500",
		"# TYPE workflow_workflows_created_total counter",
		"# HELP workflow_approvals_pending",
		"# TYPE workflow_approvals_pending gauge",
	}

	for _, c := range checks {
		if !strings.Contains(body, c) {
			t.Errorf("missing: %q", c)
		}
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
}
