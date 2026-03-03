package store_test

import (
	"os"
	"testing"
	"time"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"
	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GO_WORKFLOW_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://localhost:5432/go_workflow_test?sslmode=disable"
	}
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Skip("postgres unavailable:", err)
	}
	if err := db.Ping(); err != nil {
		t.Skip("postgres unavailable:", err)
	}
	db.Close()
	return dsn
}

// setupQueue creates a StepQueue and cleans the table before each test.
func setupQueue(t *testing.T) *store.StepQueue {
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

	// Clean up queue table before test.
	db, _ := sqlx.Open("pgx", dsn)
	db.MustExec("DELETE FROM step_queue")
	db.Close()

	return q
}

func makeItem(wfID, stepID, kind string, priority int) workflow.QueueItem {
	return workflow.QueueItem{
		WorkflowID: wfID,
		StepID:     stepID,
		StepKind:   kind,
		Priority:   priority,
	}
}

func TestStepQueue_EnqueueAndDequeue(t *testing.T) {
	q := setupQueue(t)

	item := makeItem("wf-1", "step-a", "tool", 0)
	if err := q.Enqueue(item); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

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
	q := setupQueue(t)

	if err := q.Enqueue(makeItem("wf-2", "step-b", "llm", 0)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

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
	q := setupQueue(t)

	if err := q.Enqueue(makeItem("wf-3", "step-c", "tool", 0)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, ok := q.Dequeue("worker-1", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false")
	}

	if err := q.Fail(got.ID, "something went wrong"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
}

func TestStepQueue_Heartbeat(t *testing.T) {
	q := setupQueue(t)

	if err := q.Enqueue(makeItem("wf-4", "step-d", "tool", 0)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, ok := q.Dequeue("worker-1", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false")
	}

	if err := q.Heartbeat(got.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
}

func TestStepQueue_ReapStale(t *testing.T) {
	q := setupQueue(t)

	if err := q.Enqueue(makeItem("wf-5", "step-e", "tool", 0)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

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
