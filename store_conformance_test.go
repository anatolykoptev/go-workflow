package workflow

import (
	"fmt"
	"sync"
	"testing"
)

// runConformanceTests exercises ALL StoreBackend contract requirements.
// Each backend should wire itself into this function to prove compliance.
func runConformanceTests(t *testing.T, name string, newStore func(t *testing.T) *WorkflowStore) {
	t.Helper()

	t.Run(name+"/SaveLoad", func(t *testing.T) {
		store := newStore(t)

		wf := NewWorkflow("wf-sl", "SaveLoad Test", "user:1", []Step{
			{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
			{ID: "s2", Kind: StepLLM, Config: map[string]any{"model": "gpt"}, State: StepPending, DependsOn: []string{"s1"}},
		})

		if err := store.Save(wf); err != nil {
			t.Fatalf("Save: %v", err)
		}

		loaded, ok := store.Load("wf-sl")
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
	})

	t.Run(name+"/LoadNotFound", func(t *testing.T) {
		store := newStore(t)

		loaded, ok := store.Load("nonexistent-id")
		if ok {
			t.Error("Load returned true for nonexistent workflow")
		}
		if loaded != nil {
			t.Error("Load returned non-nil workflow for nonexistent ID")
		}
	})

	t.Run(name+"/Delete", func(t *testing.T) {
		store := newStore(t)

		wf := NewWorkflow("wf-del", "Delete Test", "", nil)
		if err := store.Save(wf); err != nil {
			t.Fatalf("Save: %v", err)
		}

		if err := store.Delete("wf-del"); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		if _, ok := store.Load("wf-del"); ok {
			t.Error("workflow still present after Delete")
		}
	})

	t.Run(name+"/DeleteNonexistent", func(t *testing.T) {
		store := newStore(t)

		// Deleting a nonexistent workflow should not return an error.
		if err := store.Delete("does-not-exist"); err != nil {
			t.Errorf("Delete nonexistent returned error: %v", err)
		}
	})

	t.Run(name+"/ListByState", func(t *testing.T) {
		store := newStore(t)

		wf1 := NewWorkflow("wf-ls1", "Pending", "", nil)
		// wf1 defaults to StatePending

		wf2 := NewWorkflow("wf-ls2", "Running", "", nil)
		wf2.State = StateRunning

		if err := store.Save(wf1); err != nil {
			t.Fatalf("Save wf1: %v", err)
		}
		if err := store.Save(wf2); err != nil {
			t.Fatalf("Save wf2: %v", err)
		}

		all := store.List("")
		if len(all) != 2 {
			t.Errorf("List('') = %d, want 2", len(all))
		}

		running := store.List(StateRunning)
		if len(running) != 1 {
			t.Errorf("List(Running) = %d, want 1", len(running))
		}
		if len(running) == 1 && running[0].ID != "wf-ls2" {
			t.Errorf("List(Running)[0].ID = %q, want %q", running[0].ID, "wf-ls2")
		}
	})

	t.Run(name+"/ListByOwner", func(t *testing.T) {
		store := newStore(t)

		if err := store.Save(NewWorkflow("wf-lo1", "A", "alice", nil)); err != nil {
			t.Fatalf("Save wf1: %v", err)
		}
		if err := store.Save(NewWorkflow("wf-lo2", "B", "bob", nil)); err != nil {
			t.Fatalf("Save wf2: %v", err)
		}
		if err := store.Save(NewWorkflow("wf-lo3", "C", "alice", nil)); err != nil {
			t.Fatalf("Save wf3: %v", err)
		}

		alice := store.ListByOwner("alice")
		if len(alice) != 2 {
			t.Errorf("ListByOwner(alice) = %d, want 2", len(alice))
		}

		bob := store.ListByOwner("bob")
		if len(bob) != 1 {
			t.Errorf("ListByOwner(bob) = %d, want 1", len(bob))
		}

		nobody := store.ListByOwner("nobody")
		if len(nobody) != 0 {
			t.Errorf("ListByOwner(nobody) = %d, want 0", len(nobody))
		}
	})

	t.Run(name+"/Modify", func(t *testing.T) {
		store := newStore(t)

		wf := NewWorkflow("wf-mod", "Modify Test", "", nil)
		if err := store.Save(wf); err != nil {
			t.Fatalf("Save: %v", err)
		}

		err := store.Modify("wf-mod", func(w *Workflow) {
			w.State = StateRunning
		})
		if err != nil {
			t.Fatalf("Modify: %v", err)
		}

		loaded, ok := store.Load("wf-mod")
		if !ok {
			t.Fatal("Load after Modify returned false")
		}
		if loaded.State != StateRunning {
			t.Errorf("State = %q, want %q", loaded.State, StateRunning)
		}
	})

	t.Run(name+"/ModifyNotFound", func(t *testing.T) {
		store := newStore(t)

		err := store.Modify("nonexistent", func(w *Workflow) {
			w.State = StateRunning
		})
		if err == nil {
			t.Error("Modify nonexistent did not return error")
		}
	})

	t.Run(name+"/FindByIdempotencyKey", func(t *testing.T) {
		store := newStore(t)

		wf := NewWorkflow("wf-idem", "Idempotency Test", "", nil)
		wf.IdempotencyKey = "unique-key-123"
		wf.State = StateRunning

		if err := store.Save(wf); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// Should find the running workflow by idempotency key.
		found := store.FindByIdempotencyKey("unique-key-123")
		if found == nil {
			t.Fatal("FindByIdempotencyKey returned nil for existing running workflow")
		}
		if found.ID != "wf-idem" {
			t.Errorf("ID = %q, want %q", found.ID, "wf-idem")
		}

		// Transition to completed (terminal) — should no longer be found.
		err := store.Modify("wf-idem", func(w *Workflow) {
			w.State = StateCompleted
		})
		if err != nil {
			t.Fatalf("Modify: %v", err)
		}

		notFound := store.FindByIdempotencyKey("unique-key-123")
		if notFound != nil {
			t.Errorf("FindByIdempotencyKey returned non-nil for completed workflow: %+v", notFound)
		}
	})

	t.Run(name+"/CloneIsolation", func(t *testing.T) {
		store := newStore(t)

		wf := NewWorkflow("wf-clone", "Clone Test", "", []Step{
			{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "read"}, State: StepPending},
		})
		if err := store.Save(wf); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// Load a copy and mutate it.
		copy1, _ := store.Load("wf-clone")
		copy1.Name = "MUTATED"
		copy1.Steps[0].State = StepCompleted

		// Reload: original must be unchanged.
		copy2, ok := store.Load("wf-clone")
		if !ok {
			t.Fatal("Load returned false after mutation of copy")
		}
		if copy2.Name != "Clone Test" {
			t.Errorf("Name = %q, want %q (mutation leaked)", copy2.Name, "Clone Test")
		}
		if copy2.Steps[0].State != StepPending {
			t.Errorf("Steps[0].State = %q, want %q (mutation leaked)", copy2.Steps[0].State, StepPending)
		}
	})

	t.Run(name+"/ConcurrentAccess", func(t *testing.T) {
		store := newStore(t)

		wf := NewWorkflow("wf-conc", "Concurrent Test", "", nil)
		if err := store.Save(wf); err != nil {
			t.Fatalf("Save: %v", err)
		}

		const goroutines = 10
		var wg sync.WaitGroup
		wg.Add(goroutines)

		for i := range goroutines {
			go func(idx int) {
				defer wg.Done()

				// Modify sets state to running with a unique error tag.
				_ = store.Modify("wf-conc", func(w *Workflow) {
					w.State = StateRunning
					w.Error = fmt.Sprintf("goroutine-%d", idx)
				})

				// Load should never panic.
				_, _ = store.Load("wf-conc")
			}(i)
		}

		wg.Wait()

		// Verify the workflow is still loadable and consistent.
		loaded, ok := store.Load("wf-conc")
		if !ok {
			t.Fatal("workflow not found after concurrent access")
		}
		if loaded.State != StateRunning {
			t.Errorf("State = %q, want %q", loaded.State, StateRunning)
		}
	})
}
