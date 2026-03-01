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

// FileBackend stores workflows as JSON files in a directory.
// Each workflow is written to {dir}/{id}.json with atomic rename.
type FileBackend struct {
	dir       string
	workflows map[string]*Workflow
	mu        sync.RWMutex
}

// NewFileBackend creates a file-based backend in the given directory.
// Loads all existing workflow files on creation.
func NewFileBackend(dir string) (*FileBackend, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec,mnd
		return nil, fmt.Errorf("create workflow dir: %w", err)
	}

	fb := &FileBackend{
		dir:       dir,
		workflows: make(map[string]*Workflow),
	}

	if err := fb.loadAll(); err != nil {
		return nil, fmt.Errorf("load workflows: %w", err)
	}

	return fb, nil
}

// Save stores the workflow in memory and writes it to disk.
// The caller (WorkflowStore) already cloned, so FileBackend stores the pointer directly.
func (fb *FileBackend) Save(w *Workflow) error {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	fb.workflows[w.ID] = w
	return fb.writeToDisk(w)
}

// Load returns the raw workflow pointer (no clone).
// The caller (WorkflowStore) is responsible for cloning.
func (fb *FileBackend) Load(id string) (*Workflow, bool) {
	fb.mu.RLock()
	defer fb.mu.RUnlock()

	w, ok := fb.workflows[id]
	return w, ok
}

// Delete removes a workflow from memory and disk.
func (fb *FileBackend) Delete(id string) error {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	delete(fb.workflows, id)
	path := filepath.Join(fb.dir, id+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List returns raw workflow pointers filtered by state (empty = all).
func (fb *FileBackend) List(state WorkflowState) []*Workflow {
	fb.mu.RLock()
	defer fb.mu.RUnlock()

	var result []*Workflow
	for _, w := range fb.workflows {
		if state == "" || w.State == state {
			result = append(result, w)
		}
	}
	return result
}

// ListByOwner returns raw workflow pointers owned by the given session key.
func (fb *FileBackend) ListByOwner(owner string) []*Workflow {
	fb.mu.RLock()
	defer fb.mu.RUnlock()

	var result []*Workflow
	for _, w := range fb.workflows {
		if w.Owner == owner {
			result = append(result, w)
		}
	}
	return result
}

// FindByIdempotencyKey returns the first non-terminal workflow with the given key, or nil.
func (fb *FileBackend) FindByIdempotencyKey(key string) *Workflow {
	fb.mu.RLock()
	defer fb.mu.RUnlock()

	for _, w := range fb.workflows {
		if w.IdempotencyKey == key && !w.IsTerminal() {
			return w
		}
	}
	return nil
}

// Modify atomically loads a workflow, applies fn, and writes it back to disk.
func (fb *FileBackend) Modify(id string, fn func(w *Workflow)) error {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	w, ok := fb.workflows[id]
	if !ok {
		return fmt.Errorf("workflow %s not found", id)
	}
	fn(w)
	return fb.writeToDisk(w)
}

// Close is a no-op for FileBackend.
func (fb *FileBackend) Close() error {
	return nil
}

func (fb *FileBackend) writeToDisk(w *Workflow) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(fb.dir, w.ID+".json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0600); err != nil { //nolint:gosec,mnd // 0600: user-owned workflow state file
		return err
	}
	return os.Rename(tmpPath, path)
}

func (fb *FileBackend) loadAll() error {
	entries, err := os.ReadDir(fb.dir)
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

		data, err := os.ReadFile(filepath.Join(fb.dir, entry.Name()))
		if err != nil {
			continue
		}

		var w Workflow
		if err := json.Unmarshal(data, &w); err != nil {
			continue
		}

		fb.workflows[w.ID] = &w
	}

	return nil
}
