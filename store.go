package workflow

import "fmt"

// StoreBackend is the storage interface that concrete backends must implement.
// Backends handle persistence; they do NOT clone workflows — the WorkflowStore wrapper handles that.
type StoreBackend interface {
	Save(w *Workflow) error
	Load(id string) (*Workflow, bool)
	Delete(id string) error
	List(state WorkflowState) []*Workflow
	ListByOwner(owner string) []*Workflow
	FindByIdempotencyKey(key string) *Workflow
	Modify(id string, fn func(w *Workflow)) error
	Close() error
}

// WorkflowStore wraps a StoreBackend with clone-on-entry/exit semantics.
// All public methods are safe for concurrent use (thread safety is the backend's responsibility).
type WorkflowStore struct {
	backend StoreBackend
}

// NewWorkflowStore creates a WorkflowStore that delegates to the given backend.
func NewWorkflowStore(backend StoreBackend) *WorkflowStore {
	return &WorkflowStore{backend: backend}
}

// Save persists a deep copy of the workflow to the backend.
func (s *WorkflowStore) Save(w *Workflow) error {
	return s.backend.Save(w.Clone())
}

// Load returns a deep copy of the workflow with the given ID.
func (s *WorkflowStore) Load(id string) (*Workflow, bool) {
	w, ok := s.backend.Load(id)
	if !ok {
		return nil, false
	}
	return w.Clone(), true
}

// Delete removes a workflow from the backend.
func (s *WorkflowStore) Delete(id string) error {
	return s.backend.Delete(id)
}

// List returns deep copies of all workflows, optionally filtered by state.
func (s *WorkflowStore) List(state WorkflowState) []*Workflow {
	results := s.backend.List(state)
	cloned := make([]*Workflow, len(results))
	for i, w := range results {
		cloned[i] = w.Clone()
	}
	return cloned
}

// ListByOwner returns deep copies of workflows owned by the given session key.
func (s *WorkflowStore) ListByOwner(owner string) []*Workflow {
	results := s.backend.ListByOwner(owner)
	cloned := make([]*Workflow, len(results))
	for i, w := range results {
		cloned[i] = w.Clone()
	}
	return cloned
}

// FindByIdempotencyKey returns a deep copy of the first non-terminal workflow with the given key, or nil.
func (s *WorkflowStore) FindByIdempotencyKey(key string) *Workflow {
	w := s.backend.FindByIdempotencyKey(key)
	if w == nil {
		return nil
	}
	return w.Clone()
}

// Modify atomically loads a workflow, applies fn, and saves it back.
// Delegates directly to backend — the backend ensures atomicity.
func (s *WorkflowStore) Modify(id string, fn func(w *Workflow)) error {
	return s.backend.Modify(id, fn)
}

// Close releases resources held by the backend.
func (s *WorkflowStore) Close() error {
	return s.backend.Close()
}

// String returns a human-readable description of the store.
func (s *WorkflowStore) String() string {
	return fmt.Sprintf("WorkflowStore(%T)", s.backend)
}
