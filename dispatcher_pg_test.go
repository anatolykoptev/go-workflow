package workflow_test

import (
	"context"
	"os"
	"testing"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"
	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// testPgDSN returns a Postgres DSN for integration tests.
// Skips the test if Postgres is unreachable.
func testPgDSN(t *testing.T) string {
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

// setupDispatcher creates a PostgresDispatcher and cleans the step_queue table.
func setupDispatcher(t *testing.T) (*workflow.PostgresDispatcher, *store.StepQueue) {
	t.Helper()

	dsn := testPgDSN(t)

	// Ensure migrations have run.
	backend, err := store.NewPostgresBackend(dsn)
	if err != nil {
		t.Fatal("setup postgres backend:", err)
	}
	t.Cleanup(func() { backend.Close() })

	// Clean the queue table before test.
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open cleanup db:", err)
	}
	db.MustExec("DELETE FROM step_queue")
	db.Close()

	// Create queue and limiter for the dispatcher.
	q, err := store.NewStepQueue(dsn)
	if err != nil {
		t.Fatal("new step queue:", err)
	}

	cl, err := store.NewConcurrencyLimiter(dsn)
	if err != nil {
		q.Close()
		t.Fatal("new concurrency limiter:", err)
	}

	disp := workflow.NewPostgresDispatcher(q, cl)
	t.Cleanup(func() { disp.Close() })

	// Separate queue for verification (dequeue).
	verifyQ, err := store.NewStepQueue(dsn)
	if err != nil {
		t.Fatal("new step queue for verification:", err)
	}
	t.Cleanup(func() { verifyQ.Close() })

	return disp, verifyQ
}

func TestPostgresDispatcher_Dispatch(t *testing.T) {
	disp, q := setupDispatcher(t)
	ctx := context.Background()

	if err := disp.Dispatch(ctx, "wf-dispatch-1", "step-a", workflow.StepTool); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	got, ok := q.Dequeue("verifier", []string{"tool"})
	if !ok {
		t.Fatal("Dequeue returned false after Dispatch")
	}
	if got.WorkflowID != "wf-dispatch-1" {
		t.Errorf("WorkflowID = %q, want %q", got.WorkflowID, "wf-dispatch-1")
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
}

func TestPostgresDispatcher_DispatchBatch(t *testing.T) {
	disp, q := setupDispatcher(t)
	ctx := context.Background()

	stepIDs := []string{"step-b1", "step-b2", "step-b3"}
	kinds := []workflow.StepKind{workflow.StepTool, workflow.StepLLM, workflow.StepAgent}

	if err := disp.DispatchBatch(ctx, "wf-dispatch-batch", stepIDs, kinds); err != nil {
		t.Fatalf("DispatchBatch: %v", err)
	}

	// Dequeue all three; use all three kinds so they match.
	kindStrs := []string{"tool", "llm", "agent"}
	seen := make(map[string]string) // stepID -> stepKind

	for range stepIDs {
		got, ok := q.Dequeue("verifier", kindStrs)
		if !ok {
			t.Fatalf("Dequeue returned false, expected %d more items (seen %d)",
				len(stepIDs)-len(seen), len(seen))
		}
		if got.WorkflowID != "wf-dispatch-batch" {
			t.Errorf("WorkflowID = %q, want %q", got.WorkflowID, "wf-dispatch-batch")
		}
		seen[got.StepID] = got.StepKind
	}

	// Verify all three steps were enqueued.
	expected := map[string]string{
		"step-b1": "tool",
		"step-b2": "llm",
		"step-b3": "agent",
	}
	for id, wantKind := range expected {
		gotKind, ok := seen[id]
		if !ok {
			t.Errorf("step %q not dequeued", id)
			continue
		}
		if gotKind != wantKind {
			t.Errorf("step %q kind = %q, want %q", id, gotKind, wantKind)
		}
	}

	// No more items should be available.
	_, ok := q.Dequeue("verifier", kindStrs)
	if ok {
		t.Error("extra item dequeued after batch of 3")
	}
}
