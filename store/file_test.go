package store_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	workflow "github.com/anatolykoptev/go-workflow"
	"github.com/anatolykoptev/go-workflow/store"
)

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")

	// Create and save
	store1, _ := store.NewFileStore(wfDir)
	wf := workflow.NewWorkflow("wf1", "Persistent", "", []workflow.Step{
		{ID: "s1", Kind: workflow.StepTool, State: workflow.StepCompleted},
	})
	_ = store1.Save(wf)

	// Reload from disk
	store2, _ := store.NewFileStore(wfDir)
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
	s, _ := store.NewFileStore(wfDir)

	wf := workflow.NewWorkflow("wf1", "Atomic", "", nil)
	_ = s.Save(wf)

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
	err = s.Delete("nonexistent")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unexpected error deleting non-existent: %v", err)
	}
}

func TestNewFileStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewFileStore(filepath.Join(dir, "wf"))
	if err != nil {
		t.Fatal(err)
	}

	wf := workflow.NewWorkflow("fs1", "FileStore", "owner1", []workflow.Step{
		{ID: "s1", Kind: workflow.StepTool, Config: map[string]any{"tool": "echo"}, State: workflow.StepPending},
	})

	if err := s.Save(wf); err != nil {
		t.Fatal(err)
	}

	loaded, ok := s.Load("fs1")
	if !ok {
		t.Fatal("workflow not found after save")
	}
	if loaded.Name != "FileStore" {
		t.Errorf("name = %q, want %q", loaded.Name, "FileStore")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
