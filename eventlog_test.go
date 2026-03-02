package workflow

import (
	"path/filepath"
	"testing"
)

func TestEventLog_AppendAndLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	el, err := NewEventLog(dir)
	if err != nil {
		t.Fatalf("NewEventLog: %v", err)
	}

	events := []Event{
		{Type: EventWFStarted, WorkflowID: "wf-1", Timestamp: 1000},
		{Type: EventStepStarted, WorkflowID: "wf-1", StepID: "s1", StepKind: "tool", Timestamp: 1001},
		{Type: EventStepFinished, WorkflowID: "wf-1", StepID: "s1", StepKind: "tool", Timestamp: 1050, DurationMS: 49},
		{Type: EventWFCompleted, WorkflowID: "wf-1", Timestamp: 1051},
	}

	for _, e := range events {
		if err := el.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	loaded, err := LoadEventLog(filepath.Join(dir, "wf-1.jsonl"))
	if err != nil {
		t.Fatalf("LoadEventLog: %v", err)
	}
	if len(loaded) != 4 {
		t.Fatalf("got %d events, want 4", len(loaded))
	}
	if loaded[0].Type != EventWFStarted {
		t.Errorf("event[0].Type = %q, want workflow_started", loaded[0].Type)
	}
	if loaded[2].DurationMS != 49 {
		t.Errorf("event[2].DurationMS = %d, want 49", loaded[2].DurationMS)
	}
}

func TestEventLog_Path(t *testing.T) {
	t.Parallel()
	el := &EventLog{dir: "/tmp/logs"}
	got := el.Path("wf-123")
	if got != "/tmp/logs/wf-123.jsonl" {
		t.Errorf("path = %q", got)
	}
}

func TestEventLog_AutoTimestamp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	el, _ := NewEventLog(dir)

	err := el.Append(Event{Type: EventWFStarted, WorkflowID: "wf-ts"})
	if err != nil {
		t.Fatal(err)
	}

	loaded, _ := LoadEventLog(filepath.Join(dir, "wf-ts.jsonl"))
	if len(loaded) != 1 {
		t.Fatal("expected 1 event")
	}
	if loaded[0].Timestamp == 0 {
		t.Error("timestamp should be auto-set")
	}
}

func TestEventLog_MultipleWorkflows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	el, _ := NewEventLog(dir)

	_ = el.Append(Event{Type: EventWFStarted, WorkflowID: "wf-a", Timestamp: 1})
	_ = el.Append(Event{Type: EventWFStarted, WorkflowID: "wf-b", Timestamp: 2})
	_ = el.Append(Event{Type: EventWFCompleted, WorkflowID: "wf-a", Timestamp: 3})

	a, _ := LoadEventLog(filepath.Join(dir, "wf-a.jsonl"))
	b, _ := LoadEventLog(filepath.Join(dir, "wf-b.jsonl"))

	if len(a) != 2 {
		t.Errorf("wf-a: got %d events, want 2", len(a))
	}
	if len(b) != 1 {
		t.Errorf("wf-b: got %d events, want 1", len(b))
	}
}

func TestReplayTrace_FullCycle(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Type: EventWFStarted, WorkflowID: "wf-1", Timestamp: 1000},
		{Type: EventStepStarted, WorkflowID: "wf-1", StepID: "s1", StepKind: "tool", Timestamp: 1001},
		{Type: EventStepFinished, WorkflowID: "wf-1", StepID: "s1", DurationMS: 49, Timestamp: 1050},
		{Type: EventStepStarted, WorkflowID: "wf-1", StepID: "s2", StepKind: "llm", Timestamp: 1051},
		{Type: EventStepRetried, WorkflowID: "wf-1", StepID: "s2", Timestamp: 1060},
		{Type: EventStepFinished, WorkflowID: "wf-1", StepID: "s2", DurationMS: 100, Timestamp: 1160},
		{Type: EventWFCompleted, WorkflowID: "wf-1", Timestamp: 1161},
	}

	trace := ReplayTrace(events)

	if trace.WorkflowID != "wf-1" {
		t.Errorf("workflow_id = %q", trace.WorkflowID)
	}
	if trace.TotalMS != 161 {
		t.Errorf("total_ms = %d, want 161", trace.TotalMS)
	}
	if len(trace.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(trace.Steps))
	}
	if trace.Steps[0].StepID != "s1" {
		t.Errorf("steps[0].id = %q, want s1", trace.Steps[0].StepID)
	}
	if trace.Steps[1].Retries != 1 {
		t.Errorf("steps[1].retries = %d, want 1", trace.Steps[1].Retries)
	}
}

func TestReplayTrace_WithError(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Type: EventWFStarted, WorkflowID: "wf-err", Timestamp: 1000},
		{Type: EventStepStarted, WorkflowID: "wf-err", StepID: "s1", StepKind: "tool", Timestamp: 1001},
		{Type: EventStepFailed, WorkflowID: "wf-err", StepID: "s1", Error: "boom", DurationMS: 5, Timestamp: 1006},
		{Type: EventWFFailed, WorkflowID: "wf-err", Error: "step s1 failed: boom", Timestamp: 1007},
	}

	trace := ReplayTrace(events)

	if trace.Error != "step s1 failed: boom" {
		t.Errorf("trace.error = %q", trace.Error)
	}
	if trace.Steps[0].Error != "boom" {
		t.Errorf("step.error = %q", trace.Steps[0].Error)
	}
}

func TestReplayTrace_Empty(t *testing.T) {
	t.Parallel()
	trace := ReplayTrace(nil)
	if trace.WorkflowID != "" {
		t.Error("empty events should produce empty trace")
	}
}

func TestReplayTrace_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	el, _ := NewEventLog(dir)

	events := []Event{
		{Type: EventWFStarted, WorkflowID: "wf-rt", Timestamp: 100},
		{Type: EventStepStarted, WorkflowID: "wf-rt", StepID: "a", StepKind: "tool", Timestamp: 101},
		{Type: EventStepFinished, WorkflowID: "wf-rt", StepID: "a", DurationMS: 10, Timestamp: 111},
		{Type: EventWFCompleted, WorkflowID: "wf-rt", Timestamp: 112},
	}
	for _, e := range events {
		_ = el.Append(e)
	}

	loaded, err := LoadEventLog(el.Path("wf-rt"))
	if err != nil {
		t.Fatal(err)
	}
	trace := ReplayTrace(loaded)

	if trace.WorkflowID != "wf-rt" {
		t.Errorf("id = %q", trace.WorkflowID)
	}
	if trace.TotalMS != 12 {
		t.Errorf("total = %d, want 12", trace.TotalMS)
	}
	if len(trace.Steps) != 1 || trace.Steps[0].StepID != "a" {
		t.Errorf("steps = %v", trace.Steps)
	}
}
