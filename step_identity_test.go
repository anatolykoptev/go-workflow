package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ctxCapturingRunner records the identity carried in the ctx it is called
// with — the seam where ToolExecutor stamps the workflow owner.
type ctxCapturingRunner struct {
	got string
}

func (r *ctxCapturingRunner) Execute(ctx context.Context, _ string, _ map[string]any) (string, error) {
	r.got = mcpIdentityFrom(ctx)
	return "ok", nil
}

func toolStepExec(runner ToolRunner, owner, runAs string) (*ctxCapturingRunner, error) {
	step := &Step{ID: "s1", Kind: StepTool, Config: map[string]any{
		"tool": "t", "args": map[string]any{},
	}}
	if runAs != "" {
		step.Config["run_as"] = runAs
	}
	wf := &Workflow{ID: "w1", Owner: owner, Context: map[string]any{}}
	err := NewToolExecutor(runner).Execute(context.Background(), step, wf)
	return runner.(*ctxCapturingRunner), err
}

func TestToolStep_StampsOwnerIdentity(t *testing.T) {
	r := &ctxCapturingRunner{}
	got, err := toolStepExec(r, "fiesta", "")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.got != "fiesta" {
		t.Errorf("identity = %q, want workflow owner", got.got)
	}
}

func TestToolStep_RunAsOperator_SkipsStamp(t *testing.T) {
	r := &ctxCapturingRunner{}
	got, err := toolStepExec(r, "fiesta", "operator")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.got != "" {
		t.Errorf("run_as=operator must not stamp owner, got %q", got.got)
	}
}

func TestToolStep_EmptyOwner_NoStamp(t *testing.T) {
	r := &ctxCapturingRunner{}
	got, err := toolStepExec(r, "", "")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.got != "" {
		t.Errorf("ownerless workflow must not stamp, got %q", got.got)
	}
}

func TestHeaderTransport_StampsIdentityFromCtx(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(mcpIdentityHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &headerTransport{headers: map[string]string{"X-Static": "v"}}
	req, _ := http.NewRequestWithContext(withMCPIdentity(context.Background(), "masha"), http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	defer resp.Body.Close()
	if gotHeader != "masha" {
		t.Errorf("%s = %q, want masha", mcpIdentityHeader, gotHeader)
	}
}
