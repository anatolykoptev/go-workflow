package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// WorkflowStore provides thread-safe in-memory storage with JSON file persistence.
// Each workflow is stored as a separate file: {dir}/{id}.json
type WorkflowStore struct {
	dir       string
	workflows map[string]*Workflow
	mu        sync.RWMutex
}

// NewWorkflowStore creates a store backed by the given directory.
// Loads all existing workflow files on creation.
func NewWorkflowStore(dir string) (*WorkflowStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec,mnd
		return nil, fmt.Errorf("create workflow dir: %w", err)
	}

	s := &WorkflowStore{
		dir:       dir,
		workflows: make(map[string]*Workflow),
	}

	if err := s.loadAll(); err != nil {
		return nil, fmt.Errorf("load workflows: %w", err)
	}

	return s, nil
}

// Save persists a copy of the workflow to memory and disk (atomic write).
func (s *WorkflowStore) Save(w *Workflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := w.clone()
	s.workflows[cp.ID] = cp
	return s.writeToDisk(cp)
}

// Load returns a deep copy of the workflow with the given ID.
func (s *WorkflowStore) Load(id string) (*Workflow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w, ok := s.workflows[id]
	if !ok {
		return nil, false
	}
	return w.clone(), true
}

// Delete removes a workflow from memory and disk.
func (s *WorkflowStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.workflows, id)
	path := filepath.Join(s.dir, id+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List returns copies of all workflows, optionally filtered by state.
func (s *WorkflowStore) List(state WorkflowState) []*Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Workflow
	for _, w := range s.workflows {
		if state == "" || w.State == state {
			result = append(result, w.clone())
		}
	}
	return result
}

// ListByOwner returns copies of workflows owned by the given session key.
func (s *WorkflowStore) ListByOwner(owner string) []*Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Workflow
	for _, w := range s.workflows {
		if w.Owner == owner {
			result = append(result, w.clone())
		}
	}
	return result
}

// Modify atomically loads a workflow, applies fn to it, and saves it back.
// This is the safe way to mutate a workflow from concurrent goroutines.
func (s *WorkflowStore) Modify(id string, fn func(w *Workflow)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.workflows[id]
	if !ok {
		return fmt.Errorf("workflow %s not found", id)
	}
	fn(w)
	return s.writeToDisk(w)
}

func (s *WorkflowStore) writeToDisk(w *Workflow) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(s.dir, w.ID+".json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0600); err != nil { //nolint:gosec,mnd // 0600: user-owned workflow state file
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *WorkflowStore) loadAll() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}

		var w Workflow
		if err := json.Unmarshal(data, &w); err != nil {
			continue
		}

		s.workflows[w.ID] = &w
	}

	return nil
}
