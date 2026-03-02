package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- shared mock runners ---

type mockToolRunner struct {
	results map[string]string
	err     error
}

func (m *mockToolRunner) Execute(_ context.Context, name string, args map[string]any) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return "result from " + name, nil
}

type countingToolRunner struct {
	callCount *int
	failUntil int // fail the first N calls
}

func (r *countingToolRunner) Execute(_ context.Context, name string, args map[string]any) (string, error) {
	*r.callCount++
	if *r.callCount <= r.failUntil {
		return "", fmt.Errorf("attempt %d failed", *r.callCount)
	}
	return "result from " + name, nil
}

type selectiveToolRunner struct {
	failTools map[string]bool
}

func (r *selectiveToolRunner) Execute(_ context.Context, name string, args map[string]any) (string, error) {
	if r.failTools[name] {
		return "", fmt.Errorf("tool %s failed", name)
	}
	return "result from " + name, nil
}

type slowToolRunner struct {
	delay time.Duration
}

func (s *slowToolRunner) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	select {
	case <-time.After(s.delay):
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// --- test-only in-memory backend ---

// memBackend is a minimal in-memory StoreBackend for root package tests.
// It avoids importing the store/ subpackage (which would cause an import cycle).
type memBackend struct {
	mu sync.RWMutex
	wf map[string]*Workflow
}

func (m *memBackend) Save(w *Workflow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wf[w.ID] = w
	return nil
}

func (m *memBackend) Load(id string) (*Workflow, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w, ok := m.wf[id]
	if !ok {
		return nil, false
	}
	return w.Clone(), true
}

func (m *memBackend) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.wf, id)
	return nil
}

func (m *memBackend) List(state WorkflowState) []*Workflow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Workflow
	for _, w := range m.wf {
		if state == "" || w.State == state {
			out = append(out, w.Clone())
		}
	}
	return out
}

func (m *memBackend) ListByOwner(owner string) []*Workflow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Workflow
	for _, w := range m.wf {
		if w.Owner == owner {
			out = append(out, w.Clone())
		}
	}
	return out
}

func (m *memBackend) FindByIdempotencyKey(key string) *Workflow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, w := range m.wf {
		if w.IdempotencyKey == key && !w.IsTerminal() {
			return w.Clone()
		}
	}
	return nil
}

func (m *memBackend) Modify(id string, fn func(w *Workflow)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.wf[id]
	if !ok {
		return fmt.Errorf("workflow %s not found", id)
	}
	fn(w)
	return nil
}

func (m *memBackend) Close() error { return nil }

// --- shared helpers ---

func newTestStore(t *testing.T) *WorkflowStore {
	t.Helper()
	return NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
}

func newTestEngine(t *testing.T, runner ToolRunner) (*Engine, *WorkflowStore) {
	t.Helper()
	store := newTestStore(t)
	m := NewMetrics()
	executors := map[StepKind]StepExecutor{
		StepTool:      NewToolExecutor(runner),
		StepCondition: NewConditionExecutor(),
		StepApproval:  NewApprovalExecutor(),
	}
	engine := &Engine{store: store, metrics: m, executors: executors}
	return engine, store
}
