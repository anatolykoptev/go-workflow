package workflow

import (
	"context"
	"testing"
	"time"
)

func TestStepListener_Receive(t *testing.T) {
	dsn := newTestDB(t)

	l, err := NewStepListener(dsn)
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch := l.Listen(ctx)

	go func() {
		time.Sleep(100 * time.Millisecond)
		l.notify("wf-1:s1")
	}()

	select {
	case event := <-ch:
		if event.WorkflowID != "wf-1" || event.StepID != "s1" {
			t.Fatalf("wrong event: %+v", event)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for notification")
	}
}

func TestStepListener_MultipleEvents(t *testing.T) {
	dsn := newTestDB(t)

	l, err := NewStepListener(dsn)
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch := l.Listen(ctx)

	go func() {
		time.Sleep(100 * time.Millisecond)
		l.notify("wf-a:step-1")
		l.notify("wf-b:step-2")
	}()

	expected := []StepDoneEvent{
		{WorkflowID: "wf-a", StepID: "step-1"},
		{WorkflowID: "wf-b", StepID: "step-2"},
	}

	for i, want := range expected {
		select {
		case got := <-ch:
			if got.WorkflowID != want.WorkflowID || got.StepID != want.StepID {
				t.Fatalf("event %d: got %+v, want %+v", i, got, want)
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
}

func TestStepListener_ContextCancel(t *testing.T) {
	dsn := newTestDB(t)

	l, err := NewStepListener(dsn)
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := l.Listen(ctx)

	// Cancel immediately — channel should close without blocking.
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

func TestParsePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantOK  bool
		wantWF  string
		wantS   string
	}{
		{"valid", "wf-1:s1", true, "wf-1", "s1"},
		{"colons_in_step", "wf-1:s:extra", true, "wf-1", "s:extra"},
		{"empty", "", false, "", ""},
		{"no_colon", "wf1s1", false, "", ""},
		{"empty_wf", ":s1", false, "", ""},
		{"empty_step", "wf-1:", false, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, ok := parsePayload(tt.payload)
			if ok != tt.wantOK {
				t.Fatalf("parsePayload(%q) ok = %v, want %v", tt.payload, ok, tt.wantOK)
			}
			if ok {
				if event.WorkflowID != tt.wantWF {
					t.Errorf("WorkflowID = %q, want %q", event.WorkflowID, tt.wantWF)
				}
				if event.StepID != tt.wantS {
					t.Errorf("StepID = %q, want %q", event.StepID, tt.wantS)
				}
			}
		})
	}
}
