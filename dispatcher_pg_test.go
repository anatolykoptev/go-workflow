package workflow_test

import (
	"context"
	"testing"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"
)

// setupDispatcher creates a PostgresDispatcher and the backing PostgresBackend
// (so callers can save the parent workflow required by the
// step_queue→workflows FK) against a fresh, isolated per-test database. See
// testdb_test.go.
func setupDispatcher(t *testing.T) (*workflow.PostgresDispatcher, *store.StepQueue, *store.PostgresBackend) {
	t.Helper()

	dsn := newTestDB(t)

	// Ensure migrations have run.
	backend, err := store.NewPostgresBackend(dsn)
	if err != nil {
		t.Fatal("setup postgres backend:", err)
	}
	t.Cleanup(func() { backend.Close() })

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

	return disp, verifyQ, backend
}

// mustSaveParentWorkflow inserts the parent workflow row required by the
// step_queue_workflow_id_fkey foreign key before a Dispatch enqueues steps for
// it. Idempotent upsert (ON CONFLICT). Stale rows for wfID from a previous
// crashed run are removed first, and a t.Cleanup removes this test's rows
// after it finishes. Each test runs against its own isolated database (see
// testdb_test.go), so there is no cross-test contention.
func mustSaveParentWorkflow(t *testing.T, backend *store.PostgresBackend, wfID string) {
	t.Helper()
	if err := backend.Delete(wfID); err != nil {
		t.Fatalf("pre-clean workflow %s: %v", wfID, err)
	}
	wf := workflow.NewWorkflow(wfID, "dispatcher_test", "test", nil)
	if err := backend.Save(wf); err != nil {
		t.Fatalf("save parent workflow %s: %v", wfID, err)
	}
	t.Cleanup(func() { _ = backend.Delete(wfID) })
}

func TestPostgresDispatcher_Dispatch(t *testing.T) {
	disp, q, backend := setupDispatcher(t)
	ctx := context.Background()

	mustSaveParentWorkflow(t, backend, "wf-dispatch-1")

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
	disp, q, backend := setupDispatcher(t)
	ctx := context.Background()

	mustSaveParentWorkflow(t, backend, "wf-dispatch-batch")

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
