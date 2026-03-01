package workflow

import (
	"context"
	"fmt"
	"path/filepath"
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

// --- shared helpers ---

func newTestStore(t *testing.T) *WorkflowStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewWorkflowStore(filepath.Join(dir, "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newTestEngine(t *testing.T, runner ToolRunner) (*Engine, *WorkflowStore) {
	t.Helper()
	store := newTestStore(t)
	executors := map[StepKind]StepExecutor{
		StepTool:      NewToolExecutor(runner),
		StepCondition: NewConditionExecutor(),
		StepApproval:  NewApprovalExecutor(),
	}
	engine := &Engine{store: store, executors: executors}
	return engine, store
}
