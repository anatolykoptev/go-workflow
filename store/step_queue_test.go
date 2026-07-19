package store_test

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"
	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"
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

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GO_WORKFLOW_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://localhost:5432/go_workflow_test?sslmode=disable"
	}
	if msg := requireTestDBName(dsn); msg != "" {
		t.Fatalf("test-DB isolation guard: %s", msg)
	}
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Skip("postgres unavailable:", err)
	}
	if err := db.Ping(); err != nil {
		t.Skip("postgres unavailable:", err)
	}
	db.Close()
	// Serialize DB-backed tests across the `store` and `workflow` test binaries
	// (they run in parallel by default and share one database). See dblock_test.go.
	lockDB(t, dsn)
	return dsn
}

// setupQueue creates a StepQueue and the backing PostgresBackend (so callers
// can save the parent workflow required by the step_queue→workflows FK).
//
// Cleanup is scoped per Enqueue (see mustEnqueue): no global DELETE is issued
// here, which would race with the workflow package's DB tests running in a
// parallel test binary against the same database.
func setupQueue(t *testing.T) (*store.StepQueue, *store.PostgresBackend) {
	t.Helper()
	dsn := testDSN(t)

	// Ensure migration has run via PostgresBackend (creates both tables).
	backend, err := store.NewPostgresBackend(dsn)
	if err != nil {
		t.Fatal("setup postgres backend:", err)
	}
	t.Cleanup(func() { backend.Close() })

	q, err := store.NewStepQueue(dsn)
	if err != nil {
		t.Fatal("new step queue:", err)
	}
	t.Cleanup(func() { q.Close() })

	return q, backend
}

func makeItem(wfID, stepID, kind string, priority int) workflow.QueueItem {
	return workflow.QueueItem{
		WorkflowID: wfID,
		StepID:     stepID,
		StepKind:   kind,
		Priority:   priority,
	}
}

// mustEnqueue saves a minimal parent workflow for item.WorkflowID (required by
// the step_queue_workflow_id_fkey foreign key — a step must belong to a real
// workflow) and then enqueues the item. The parent save is an idempotent
// upsert (ON CONFLICT), so repeated calls for the same workflow are safe.
//
// Stale rows for item.WorkflowID from a previous crashed run are removed
// first (scoped — never a global DELETE that would race with concurrent DB
// tests in the workflow package), and a t.Cleanup removes this test's rows
// after it finishes.
func mustEnqueue(t *testing.T, q *store.StepQueue, backend *store.PostgresBackend, item workflow.QueueItem) {
	t.Helper()

	// Scoped cleanup of any stale rows for this workflow (survivors of a
	// previous crashed run). step_queue rows cascade-delete with the parent.
	cleanupWorkflow(t, backend, item.WorkflowID)

	// Insert the parent workflow required by the FK.
	wf := workflow.NewWorkflow(item.WorkflowID, "step_queue_test", "test", nil)
	if err := backend.Save(wf); err != nil {
		t.Fatalf("save parent workflow %s: %v", item.WorkflowID, err)
	}

	if err := q.Enqueue(item); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

// cleanupWorkflow removes the workflow row (and its cascaded step_queue rows)
// for the given id both now and at test end. Scoped to one id so concurrent DB
// tests in other packages sharing the same database do not race.
func cleanupWorkflow(t *testing.T, backend *store.PostgresBackend, wfID string) {
	t.Helper()
	if err := backend.Delete(wfID); err != nil {
		t.Fatalf("pre-clean workflow %s: %v", wfID, err)
	}
	t.Cleanup(func() { _ = backend.Delete(wfID) })
}

func TestStepQueue_EnqueueAndDequeue(t *testing.T) {
	q, backend := setupQueue(t)

	item := makeItem("wf-1", "step-a", "tool", 0)
	mustEnqueue(t, q, backend, item)

	got, ok := q.Dequeue("worker-1", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false, expected item")
	}
	if got.WorkflowID != "wf-1" {
		t.Errorf("WorkflowID = %q, want %q", got.WorkflowID, "wf-1")
	}
	if got.StepID != "step-a" {
		t.Errorf("StepID = %q, want %q", got.StepID, "step-a")
	}
	if got.StepKind != "tool" {
		t.Errorf("StepKind = %q, want %q", got.StepKind, "tool")
	}
	if got.State != workflow.QueueClaimed {
		t.Errorf("State = %q, want %q", got.State, workflow.QueueClaimed)
	}
	if got.WorkerID != "worker-1" {
		t.Errorf("WorkerID = %q, want %q", got.WorkerID, "worker-1")
	}

	// Second dequeue should return nothing.
	_, ok = q.Dequeue("worker-2", []string{"tool"})
	if ok {
		t.Error("second Dequeue returned true, expected empty queue")
	}
}

func TestStepQueue_Complete(t *testing.T) {
	q, backend := setupQueue(t)

	mustEnqueue(t, q, backend, makeItem("wf-2", "step-b", "llm", 0))

	got, ok := q.Dequeue("worker-1", []string{"llm"})
	if !ok {
		t.Fatal("Dequeue returned false")
	}

	result := []byte(`{"output":"hello"}`)
	if err := q.Complete(got.ID, result, ""); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestStepQueue_Fail(t *testing.T) {
	q, backend := setupQueue(t)

	mustEnqueue(t, q, backend, makeItem("wf-3", "step-c", "tool", 0))

	got, ok := q.Dequeue("worker-1", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false")
	}

	if err := q.Fail(got.ID, "something went wrong"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
}

func TestStepQueue_Heartbeat(t *testing.T) {
	q, backend := setupQueue(t)

	mustEnqueue(t, q, backend, makeItem("wf-4", "step-d", "tool", 0))

	got, ok := q.Dequeue("worker-1", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false")
	}

	if err := q.Heartbeat(got.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
}

func TestStepQueue_ReapStale(t *testing.T) {
	q, backend := setupQueue(t)

	mustEnqueue(t, q, backend, makeItem("wf-5", "step-e", "tool", 0))

	got, ok := q.Dequeue("worker-1", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false")
	}

	// Reap with 0 timeout — everything claimed is stale.
	time.Sleep(time.Millisecond)
	n, err := q.ReapStale(0)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if n != 1 {
		t.Errorf("ReapStale count = %d, want 1", n)
	}

	// Item should be re-dequeueable.
	got2, ok := q.Dequeue("worker-2", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue after reap returned false, expected re-queued item")
	}
	if got2.ID != got.ID {
		t.Errorf("re-dequeued ID = %d, want %d", got2.ID, got.ID)
	}
	if got2.WorkerID != "worker-2" {
		t.Errorf("re-dequeued WorkerID = %q, want %q", got2.WorkerID, "worker-2")
	}
}
