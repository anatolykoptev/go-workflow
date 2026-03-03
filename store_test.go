package workflow

import (
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
