package workflow_test

import (
	"context"
	"testing"
	"time"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestDistributed_FullFlow(t *testing.T) {
	dsn := testPgDSN(t) // skips if Postgres unavailable

	// Ensure migrations run.
	pgStore, err := store.NewPostgresStore(dsn)
	if err != nil {
		t.Skip("no test db:", err)
	}
	t.Cleanup(func() { pgStore.Close() })

	// Two sequential steps: s1 -> s2. Steps MUST be initialized to StepPending
	// (NewWorkflow does not normalize step states); findAllRunnable skips any
	// step whose State != StepPending, so Advance would dispatch nothing.
	wfID := "dist-test-" + uniqueSuffix()
	wf := workflow.NewWorkflow(wfID, "distributed-test", "test", []workflow.Step{
		{ID: "s1", Kind: workflow.StepNoop, State: workflow.StepPending},
		{ID: "s2", Kind: workflow.StepNoop, State: workflow.StepPending, DependsOn: []string{"s1"}},
	})

	if err := pgStore.Save(wf); err != nil {
		t.Fatalf("save workflow: %v", err)
	}
	// Scoped cleanup: never a global DELETE that would race with the store
	// package's DB tests running in a parallel test binary against the same
	// database. Deleting the workflow cascades to its step_queue rows.
	t.Cleanup(func() { _ = pgStore.Delete(wfID) })

	// Separate queue connections for dispatcher and worker
	// because WorkerNode.Stop() closes its queue.
	dispatchQ, err := store.NewStepQueue(dsn)
	if err != nil {
		t.Fatalf("new dispatch queue: %v", err)
	}
	t.Cleanup(func() { dispatchQ.Close() })

	workerQ, err := store.NewStepQueue(dsn)
	if err != nil {
		t.Fatalf("new worker queue: %v", err)
	}

	// Dispatcher enqueues steps to step_queue.
	pgDispatcher := workflow.NewPostgresDispatcher(dispatchQ)

	// Engine with Postgres store and distributed dispatcher.
	engine := workflow.NewEngine(pgStore,
		workflow.WithDispatcher(pgDispatcher),
	)

	// Transition workflow to running state.
	if err := pgStore.Modify(wfID, func(w *workflow.Workflow) {
		w.State = workflow.StateRunning
		w.UpdatedAt = time.Now().UnixMilli()
	}); err != nil {
		t.Fatalf("set running: %v", err)
	}

	// Listener for pg_notify("step_done") events.
	listener, err := workflow.NewStepListener(dsn)
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start listener — advances the DAG when steps complete.
	go engine.ListenForResults(ctx, listener)

	// Worker dequeues and executes steps.
	worker, err := workflow.NewWorkerNode(workflow.WorkerConfig{
		ID:           "test-dist-worker",
		Queue:        workerQ,
		StepKinds:    []string{string(workflow.StepNoop)},
		Engine:       engine,
		PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}

	go worker.Run(ctx)
	defer worker.Stop()

	// Kick off: dispatch the first runnable step (s1) to the queue.
	advanced, err := engine.Advance(ctx, wfID)
	if err != nil {
		t.Fatalf("initial advance: %v", err)
	}
	if !advanced {
		t.Fatal("initial advance did not dispatch any steps")
	}

	// Poll until workflow reaches terminal state.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for distributed workflow completion")
		case <-ticker.C:
			w, ok := pgStore.Load(wfID)
			if !ok {
				t.Fatal("workflow disappeared from store")
			}
			if w.State == workflow.StateCompleted {
				// Verify both steps completed.
				for _, sid := range []string{"s1", "s2"} {
					step := w.GetStep(sid)
					if step == nil {
						t.Fatalf("step %s not found", sid)
					}
					if step.State != workflow.StepCompleted {
						t.Fatalf("step %s state = %s, want completed", sid, step.State)
					}
				}
				return // success
			}
			if w.State == workflow.StateFailed || w.State == workflow.StateCancelled {
				t.Fatalf("workflow reached terminal state %s instead of completed", w.State)
			}
		}
	}
}

// uniqueSuffix returns a time-based suffix for test isolation.
func uniqueSuffix() string {
	return time.Now().Format("20060102-150405.000")
}
