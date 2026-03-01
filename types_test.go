package workflow

import "testing"

func TestWorkflowGetStep(t *testing.T) {
	wf := NewWorkflow("wf1", "Test", "", []Step{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	})

	if s := wf.GetStep("b"); s == nil || s.ID != "b" {
		t.Error("GetStep(b) failed")
	}
	if s := wf.GetStep("z"); s != nil {
		t.Error("GetStep(z) should return nil")
	}
}

func TestWorkflowIsTerminal(t *testing.T) {
	wf := NewWorkflow("wf1", "Test", "", nil)

	wf.State = StatePending
	if wf.IsTerminal() {
		t.Error("pending should not be terminal")
	}

	wf.State = StateCompleted
	if !wf.IsTerminal() {
		t.Error("completed should be terminal")
	}

	wf.State = StateFailed
	if !wf.IsTerminal() {
		t.Error("failed should be terminal")
	}
}
