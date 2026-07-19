package store_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"
)

// runConformanceTests exercises ALL StoreBackend contract requirements.
func runConformanceTests(t *testing.T, name string, newStore func(t *testing.T) *workflow.WorkflowStore) {
	t.Helper()
	t.Run(name+"/SaveLoad", func(t *testing.T) { testSaveLoad(t, newStore(t)) })
	t.Run(name+"/LoadNotFound", func(t *testing.T) { testLoadNotFound(t, newStore(t)) })
	t.Run(name+"/Delete", func(t *testing.T) { testDelete(t, newStore(t)) })
	t.Run(name+"/DeleteNonexistent", func(t *testing.T) { testDeleteNonexistent(t, newStore(t)) })
	t.Run(name+"/ListByState", func(t *testing.T) { testListByState(t, newStore(t)) })
	t.Run(name+"/ListByOwner", func(t *testing.T) { testListByOwner(t, newStore(t)) })
	t.Run(name+"/Modify", func(t *testing.T) { testModify(t, newStore(t)) })
	t.Run(name+"/ModifyNotFound", func(t *testing.T) { testModifyNotFound(t, newStore(t)) })
	t.Run(name+"/FindByIdempotencyKey", func(t *testing.T) { testFindByIdempotencyKey(t, newStore(t)) })
	t.Run(name+"/CloneIsolation", func(t *testing.T) { testCloneIsolation(t, newStore(t)) })
	t.Run(name+"/ConcurrentAccess", func(t *testing.T) { testConcurrentAccess(t, newStore(t)) })
}

func testSaveLoad(t *testing.T, s *workflow.WorkflowStore) {
	wf := workflow.NewWorkflow("wf-sl", "SaveLoad Test", "user:1", []workflow.Step{
		{ID: "s1", Kind: workflow.StepTool, Config: map[string]any{"tool": "echo"}, State: workflow.StepPending},
		{ID: "s2", Kind: workflow.StepLLM, Config: map[string]any{"model": "gpt"}, State: workflow.StepPending, DependsOn: []string{"s1"}},
	})
	if err := s.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, ok := s.Load("wf-sl")
	if !ok {
		t.Fatal("Load returned false after Save")
	}
	if loaded.Name != "SaveLoad Test" {
		t.Errorf("Name = %q, want %q", loaded.Name, "SaveLoad Test")
	}
	if loaded.Owner != "user:1" {
		t.Errorf("Owner = %q, want %q", loaded.Owner, "user:1")
	}
	if len(loaded.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(loaded.Steps))
	}
	if loaded.Steps[0].ID != "s1" {
		t.Errorf("Steps[0].ID = %q, want %q", loaded.Steps[0].ID, "s1")
	}
	if loaded.Steps[1].ID != "s2" {
		t.Errorf("Steps[1].ID = %q, want %q", loaded.Steps[1].ID, "s2")
	}
}

func testLoadNotFound(t *testing.T, s *workflow.WorkflowStore) {
	loaded, ok := s.Load("nonexistent-id")
	if ok {
		t.Error("Load returned true for nonexistent workflow")
	}
	if loaded != nil {
		t.Error("Load returned non-nil workflow for nonexistent ID")
	}
}

func testDelete(t *testing.T, s *workflow.WorkflowStore) {
	wf := workflow.NewWorkflow("wf-del", "Delete Test", "", nil)
	if err := s.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete("wf-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Load("wf-del"); ok {
		t.Error("workflow still present after Delete")
	}
}

func testDeleteNonexistent(t *testing.T, s *workflow.WorkflowStore) {
	if err := s.Delete("does-not-exist"); err != nil {
		t.Errorf("Delete nonexistent returned error: %v", err)
	}
}

func testListByState(t *testing.T, s *workflow.WorkflowStore) {
	wf1 := workflow.NewWorkflow("wf-ls1", "Pending", "", nil)
	wf2 := workflow.NewWorkflow("wf-ls2", "Running", "", nil)
	wf2.State = workflow.StateRunning

	if err := s.Save(wf1); err != nil {
		t.Fatalf("Save wf1: %v", err)
	}
	if err := s.Save(wf2); err != nil {
		t.Fatalf("Save wf2: %v", err)
	}

	all := s.List("")
	if len(all) != 2 {
		t.Errorf("List('') = %d, want 2", len(all))
	}

	running := s.List(workflow.StateRunning)
	if len(running) != 1 {
		t.Errorf("List(Running) = %d, want 1", len(running))
	}
	if len(running) == 1 && running[0].ID != "wf-ls2" {
		t.Errorf("List(Running)[0].ID = %q, want %q", running[0].ID, "wf-ls2")
	}
}

func testListByOwner(t *testing.T, s *workflow.WorkflowStore) {
	if err := s.Save(workflow.NewWorkflow("wf-lo1", "A", "alice", nil)); err != nil {
		t.Fatalf("Save wf1: %v", err)
	}
	if err := s.Save(workflow.NewWorkflow("wf-lo2", "B", "bob", nil)); err != nil {
		t.Fatalf("Save wf2: %v", err)
	}
	if err := s.Save(workflow.NewWorkflow("wf-lo3", "C", "alice", nil)); err != nil {
		t.Fatalf("Save wf3: %v", err)
	}

	if got := s.ListByOwner("alice"); len(got) != 2 {
		t.Errorf("ListByOwner(alice) = %d, want 2", len(got))
	}
	if got := s.ListByOwner("bob"); len(got) != 1 {
		t.Errorf("ListByOwner(bob) = %d, want 1", len(got))
	}
	if got := s.ListByOwner("nobody"); len(got) != 0 {
		t.Errorf("ListByOwner(nobody) = %d, want 0", len(got))
	}
}

func testModify(t *testing.T, s *workflow.WorkflowStore) {
	wf := workflow.NewWorkflow("wf-mod", "Modify Test", "", nil)
	if err := s.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := s.Modify("wf-mod", func(w *workflow.Workflow) {
		w.State = workflow.StateRunning
	}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	loaded, ok := s.Load("wf-mod")
	if !ok {
		t.Fatal("Load after Modify returned false")
	}
	if loaded.State != workflow.StateRunning {
		t.Errorf("State = %q, want %q", loaded.State, workflow.StateRunning)
	}
}

func testModifyNotFound(t *testing.T, s *workflow.WorkflowStore) {
	err := s.Modify("nonexistent", func(w *workflow.Workflow) {
		w.State = workflow.StateRunning
	})
	if err == nil {
		t.Error("Modify nonexistent did not return error")
	}
}

func testFindByIdempotencyKey(t *testing.T, s *workflow.WorkflowStore) {
	wf := workflow.NewWorkflow("wf-idem", "Idempotency Test", "", nil)
	wf.IdempotencyKey = "unique-key-123"
	wf.State = workflow.StateRunning
	if err := s.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	found := s.FindByIdempotencyKey("unique-key-123")
	if found == nil {
		t.Fatal("FindByIdempotencyKey returned nil for existing running workflow")
	}
	if found.ID != "wf-idem" {
		t.Errorf("ID = %q, want %q", found.ID, "wf-idem")
	}

	// Transition to completed — should no longer be found.
	if err := s.Modify("wf-idem", func(w *workflow.Workflow) {
		w.State = workflow.StateCompleted
	}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	if got := s.FindByIdempotencyKey("unique-key-123"); got != nil {
		t.Errorf("FindByIdempotencyKey returned non-nil for completed workflow: %+v", got)
	}
}

func testCloneIsolation(t *testing.T, s *workflow.WorkflowStore) {
	wf := workflow.NewWorkflow("wf-clone", "Clone Test", "", []workflow.Step{
		{ID: "s1", Kind: workflow.StepTool, Config: map[string]any{"tool": "read"}, State: workflow.StepPending},
	})
	if err := s.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	copy1, _ := s.Load("wf-clone")
	copy1.Name = "MUTATED"
	copy1.Steps[0].State = workflow.StepCompleted

	copy2, ok := s.Load("wf-clone")
	if !ok {
		t.Fatal("Load returned false after mutation of copy")
	}
	if copy2.Name != "Clone Test" {
		t.Errorf("Name = %q, want %q (mutation leaked)", copy2.Name, "Clone Test")
	}
	if copy2.Steps[0].State != workflow.StepPending {
		t.Errorf("Steps[0].State = %q, want %q (mutation leaked)", copy2.Steps[0].State, workflow.StepPending)
	}
}

func testConcurrentAccess(t *testing.T, s *workflow.WorkflowStore) {
	wf := workflow.NewWorkflow("wf-conc", "Concurrent Test", "", nil)
	if err := s.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			_ = s.Modify("wf-conc", func(w *workflow.Workflow) {
				w.State = workflow.StateRunning
				w.Error = fmt.Sprintf("goroutine-%d", idx)
			})
			_, _ = s.Load("wf-conc")
		}(i)
	}

	wg.Wait()

	loaded, ok := s.Load("wf-conc")
	if !ok {
		t.Fatal("workflow not found after concurrent access")
	}
	if loaded.State != workflow.StateRunning {
		t.Errorf("State = %q, want %q", loaded.State, workflow.StateRunning)
	}
}

func TestFileBackend_Conformance(t *testing.T) {
	runConformanceTests(t, "FileBackend", func(t *testing.T) *workflow.WorkflowStore {
		t.Helper()
		dir := t.TempDir()
		s, err := store.NewFileStore(filepath.Join(dir, "workflows"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestSQLiteBackend_Conformance(t *testing.T) {
	runConformanceTests(t, "SQLiteBackend", func(t *testing.T) *workflow.WorkflowStore {
		t.Helper()
		dbPath := filepath.Join(t.TempDir(), "test.db")
		s, err := store.NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestPostgresBackend_Conformance(t *testing.T) {
	dsn := os.Getenv("WORKFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WORKFLOW_TEST_POSTGRES_DSN not set")
	}

	runConformanceTests(t, "PostgresBackend", func(t *testing.T) *workflow.WorkflowStore {
		t.Helper()
		// Serialize DB-backed tests across packages — see dblock_test.go.
		// Locked per subtest so CleanAll (global DELETE FROM workflows) cannot
		// race the workflow package's enqueues/loads against the same DB.
		lockDB(t, dsn)
		backend, err := store.NewPostgresBackend(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { backend.Close() })
		backend.CleanAll()
		return workflow.NewWorkflowStore(backend)
	})
}
