package workflow

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// requireTestDBName validates that dsn refers to a database whose name contains "_test".
// Returns a non-empty error string if the name looks like a production database.
func requireTestDBName(dsn string) string {
	if dsn == "" {
		return ""
	}
	// URL format: postgres://user:pass@host/dbname[?params]
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		dbName := strings.TrimPrefix(u.Path, "/")
		if idx := strings.IndexByte(dbName, '?'); idx >= 0 {
			dbName = dbName[:idx]
		}
		if dbName != "" && !strings.Contains(dbName, "_test") {
			return fmt.Sprintf("refusing to connect: DB name %q must contain \"_test\" (set GO_WORKFLOW_TEST_DSN to a test database)", dbName)
		}
		return ""
	}
	// Key-value format: "host=... dbname=go_workflow_test ..."
	for _, part := range strings.Fields(dsn) {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 && kv[0] == "dbname" {
			if !strings.Contains(kv[1], "_test") {
				return fmt.Sprintf("refusing to connect: DB name %q must contain \"_test\" (set GO_WORKFLOW_TEST_DSN to a test database)", kv[1])
			}
			return ""
		}
	}
	return ""
}

// testPgDSN returns the Postgres DSN for integration tests.
// Skips if Postgres is unavailable.
func testPgDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("GO_WORKFLOW_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://localhost:5432/go_workflow_test?sslmode=disable"
	}
	if msg := requireTestDBName(dsn); msg != "" {
		t.Fatalf("test-DB isolation guard: %s", msg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skip("postgres unavailable:", err)
	}
	conn.Close(ctx)

	// Serialize DB-backed tests across packages — see dblock_internal_test.go.
	// For listener tests this also prevents stray pg_notify('step_done')
	// broadcasts from the store package's StepQueue.Complete arriving mid-test.
	lockDB(t, dsn)
	return dsn
}

func TestStepListener_Receive(t *testing.T) {
	dsn := testPgDSN(t)

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
	dsn := testPgDSN(t)

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
	dsn := testPgDSN(t)

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
