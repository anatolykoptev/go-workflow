package workflow

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSaveLoad(t *testing.T) {
	store := newTestStore(t)

	wf := NewWorkflow("wf1", "Test", "telegram:123", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "read_file"}, State: StepPending},
	})

	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	loaded, ok := store.Load("wf1")
	if !ok {
		t.Fatal("workflow not found after save")
	}
	if loaded.Name != "Test" {
		t.Errorf("name = %q, want %q", loaded.Name, "Test")
	}
	if loaded.Owner != "telegram:123" {
		t.Errorf("owner = %q, want %q", loaded.Owner, "telegram:123")
	}
}

func TestStoreDelete(t *testing.T) {
	store := newTestStore(t)
	wf := NewWorkflow("wf1", "Test", "", nil)
	_ = store.Save(wf)

	if err := store.Delete("wf1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Load("wf1"); ok {
		t.Error("workflow still present after delete")
	}
}

func TestStoreList(t *testing.T) {
	store := newTestStore(t)
	_ = store.Save(NewWorkflow("wf1", "A", "", nil))

	wf2 := NewWorkflow("wf2", "B", "", nil)
	wf2.State = StateRunning
	_ = store.Save(wf2)

	all := store.List("")
	if len(all) != 2 {
		t.Errorf("list all = %d, want 2", len(all))
	}

	running := store.List(StateRunning)
	if len(running) != 1 {
		t.Errorf("list running = %d, want 1", len(running))
	}
}

func TestStoreListByOwner(t *testing.T) {
	store := newTestStore(t)
	_ = store.Save(NewWorkflow("wf1", "A", "telegram:1", nil))
	_ = store.Save(NewWorkflow("wf2", "B", "telegram:2", nil))
	_ = store.Save(NewWorkflow("wf3", "C", "telegram:1", nil))

	owned := store.ListByOwner("telegram:1")
	if len(owned) != 2 {
		t.Errorf("owned = %d, want 2", len(owned))
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")

	// Create and save
	store1, _ := NewFileStore(wfDir)
	wf := NewWorkflow("wf1", "Persistent", "", []Step{
		{ID: "s1", Kind: StepTool, State: StepCompleted},
	})
	_ = store1.Save(wf)

	// Reload from disk
	store2, _ := NewFileStore(wfDir)
	loaded, ok := store2.Load("wf1")
	if !ok {
		t.Fatal("workflow not found after reload")
	}
	if loaded.Name != "Persistent" {
		t.Errorf("name = %q, want %q", loaded.Name, "Persistent")
	}
	if len(loaded.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(loaded.Steps))
	}
}

func TestStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")
	store, _ := NewFileStore(wfDir)

	wf := NewWorkflow("wf1", "Atomic", "", nil)
	_ = store.Save(wf)

	// Check: no .tmp files left behind
	entries, _ := os.ReadDir(wfDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	// Check: file exists and is valid JSON
	path := filepath.Join(wfDir, "wf1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("workflow file is empty")
	}

	// Delete non-existent should not error
	err = store.Delete("nonexistent")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unexpected error deleting non-existent: %v", err)
	}
}

func TestFileBackend_Conformance(t *testing.T) {
	runConformanceTests(t, "FileBackend", func(t *testing.T) *WorkflowStore {
		t.Helper()
		dir := t.TempDir()
		store, err := NewFileStore(filepath.Join(dir, "workflows"))
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestNewFileStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(filepath.Join(dir, "wf"))
	if err != nil {
		t.Fatal(err)
	}

	wf := NewWorkflow("fs1", "FileStore", "owner1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
	})

	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	loaded, ok := store.Load("fs1")
	if !ok {
		t.Fatal("workflow not found after save")
	}
	if loaded.Name != "FileStore" {
		t.Errorf("name = %q, want %q", loaded.Name, "FileStore")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
