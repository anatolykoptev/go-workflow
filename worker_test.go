package workflow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockQueue is an in-memory StepWorkerQueue for unit tests.
type mockQueue struct {
	mu         sync.Mutex
	items      []*QueueItem
	completed  map[int64][]byte
	failed     map[int64]string
	heartbeats map[int64]int
	nextID     int64
	closed     bool
}

func newMockQueue() *mockQueue {
	return &mockQueue{
		completed:  make(map[int64][]byte),
		failed:     make(map[int64]string),
		heartbeats: make(map[int64]int),
	}
}

func (q *mockQueue) enqueue(item QueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	item.ID = q.nextID
	item.State = QueuePending
	q.items = append(q.items, &item)
}

func (q *mockQueue) Dequeue(workerID string, kinds []string) (*QueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	kindSet := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		kindSet[k] = true
	}

	for i, item := range q.items {
		if item.State == QueuePending && kindSet[item.StepKind] {
			q.items[i].State = QueueClaimed
			q.items[i].WorkerID = workerID
			cp := *q.items[i]
			return &cp, true
		}
	}
	return nil, false
}

func (q *mockQueue) Complete(itemID int64, result []byte, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completed[itemID] = result
	return nil
}

func (q *mockQueue) Fail(itemID int64, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed[itemID] = errMsg
	return nil
}

func (q *mockQueue) Heartbeat(itemID int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.heartbeats[itemID]++
	return nil
}

func (q *mockQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	return nil
}

func TestWorkerNode_ProcessOne(t *testing.T) {
	st := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(st)

	// Save a workflow with a noop step.
	wf := NewWorkflow("wf-worker-1", "test-worker", "test", []Step{
		{ID: "step-1", Kind: StepNoop, State: StepPending, Config: map[string]any{}},
	})
	wf.State = StateRunning
	if err := st.Save(wf); err != nil {
		t.Fatalf("save workflow: %v", err)
	}

	q := newMockQueue()
	q.enqueue(QueueItem{
		WorkflowID: "wf-worker-1",
		StepID:     "step-1",
		StepKind:   string(StepNoop),
	})

	w := &WorkerNode{
		id:      "test-worker-1",
		kinds:   []string{string(StepNoop)},
		engine:  engine,
		queue:   q,
		hbInt:   time.Second,
		pollInt: time.Second,
		logger:  engine.log(),
		stop:    make(chan struct{}),
	}

	got := w.ProcessOne(context.Background())
	if !got {
		t.Fatal("ProcessOne returned false, expected true")
	}

	// Verify step completed in store.
	loaded, ok := st.Load("wf-worker-1")
	if !ok {
		t.Fatal("workflow not found after ProcessOne")
	}
	step := loaded.GetStep("step-1")
	if step == nil {
		t.Fatal("step not found")
	}
	if step.State != StepCompleted {
		t.Errorf("step state = %q, want %q", step.State, StepCompleted)
	}

	// Queue should have marked item completed.
	q.mu.Lock()
	_, wasCompleted := q.completed[1]
	q.mu.Unlock()
	if !wasCompleted {
		t.Error("queue item was not completed")
	}

	// Empty queue returns false.
	if w.ProcessOne(context.Background()) {
		t.Error("ProcessOne returned true on empty queue")
	}
}

func TestWorkerNode_ProcessOne_Failure(t *testing.T) {
	st := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(st)

	// Save a workflow with a tool step but no tool executor registered.
	wf := NewWorkflow("wf-fail-1", "test-fail", "test", []Step{
		{ID: "step-1", Kind: StepTool, State: StepPending, Config: map[string]any{"tool": "missing"}},
	})
	wf.State = StateRunning
	if err := st.Save(wf); err != nil {
		t.Fatalf("save workflow: %v", err)
	}

	q := newMockQueue()
	q.enqueue(QueueItem{
		WorkflowID: "wf-fail-1",
		StepID:     "step-1",
		StepKind:   string(StepTool),
	})

	w := &WorkerNode{
		id:      "test-fail-worker",
		kinds:   []string{string(StepTool)},
		engine:  engine,
		queue:   q,
		hbInt:   time.Second,
		pollInt: time.Second,
		logger:  engine.log(),
		stop:    make(chan struct{}),
	}

	got := w.ProcessOne(context.Background())
	if !got {
		t.Fatal("ProcessOne returned false, expected true (even on failure)")
	}

	q.mu.Lock()
	errMsg, wasFailed := q.failed[1]
	q.mu.Unlock()
	if !wasFailed {
		t.Fatal("queue item was not failed")
	}
	if errMsg == "" {
		t.Error("expected non-empty error message")
	}
}

func TestWorkerNode_HeartbeatLoop(t *testing.T) {
	q := newMockQueue()
	st := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(st)

	w := &WorkerNode{
		id:      "test-hb-worker",
		kinds:   []string{string(StepNoop)},
		engine:  engine,
		queue:   q,
		hbInt:   10 * time.Millisecond,
		pollInt: 10 * time.Millisecond,
		logger:  engine.log(),
		stop:    make(chan struct{}),
	}

	// Simulate an active item.
	w.curID.Store(42)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	w.heartbeatLoop(ctx)

	q.mu.Lock()
	hbCount := q.heartbeats[42]
	q.mu.Unlock()

	if hbCount == 0 {
		t.Error("expected at least one heartbeat, got 0")
	}
}

func TestWorkerNode_RunStopsOnContext(t *testing.T) {
	q := newMockQueue()
	st := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(st)

	w := &WorkerNode{
		id:      "test-run-worker",
		kinds:   []string{string(StepNoop)},
		engine:  engine,
		queue:   q,
		hbInt:   50 * time.Millisecond,
		pollInt: 10 * time.Millisecond,
		logger:  engine.log(),
		stop:    make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestWorkerNode_RunStopsOnStop(t *testing.T) {
	q := newMockQueue()
	st := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(st)

	w := &WorkerNode{
		id:      "test-stop-worker",
		kinds:   []string{string(StepNoop)},
		engine:  engine,
		queue:   q,
		hbInt:   50 * time.Millisecond,
		pollInt: 10 * time.Millisecond,
		logger:  engine.log(),
		stop:    make(chan struct{}),
	}

	var done atomic.Bool
	go func() {
		w.Run(context.Background())
		done.Store(true)
	}()

	time.Sleep(30 * time.Millisecond)
	w.Stop()
	time.Sleep(50 * time.Millisecond)

	if !done.Load() {
		t.Fatal("Run did not stop after Stop() was called")
	}
}

func TestNewWorkerNode_Validation(t *testing.T) {
	q := newMockQueue()
	st := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(st)

	tests := []struct {
		name string
		cfg  WorkerConfig
	}{
		{"nil engine", WorkerConfig{ID: "w1", Queue: q, StepKinds: []string{"noop"}}},
		{"nil queue", WorkerConfig{ID: "w2", Engine: engine, StepKinds: []string{"noop"}}},
		{"empty kinds", WorkerConfig{ID: "w3", Queue: q, Engine: engine}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWorkerNode(tt.cfg)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
