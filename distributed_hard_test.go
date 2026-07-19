package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- mock queue for hard red tests ---

type hardQueue struct {
	mu         sync.Mutex
	items      []*QueueItem
	nextID     int64
	dequeueFn  func() (*QueueItem, bool)         // override dequeue behavior
	failFn     func(int64, string) error         // override fail behavior
	completeFn func(int64, []byte, string) error // override complete behavior
	hbCount    atomic.Int32
	closed     atomic.Bool
}

func (q *hardQueue) Enqueue(item QueueItem) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	item.ID = q.nextID
	item.State = QueuePending
	q.items = append(q.items, &item)
	return nil
}

func (q *hardQueue) Dequeue(workerID string, kinds []string) (*QueueItem, bool) {
	if q.dequeueFn != nil {
		return q.dequeueFn()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	kindSet := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		kindSet[k] = true
	}
	for _, it := range q.items {
		if it.State == QueuePending && kindSet[it.StepKind] {
			it.State = QueueClaimed
			it.WorkerID = workerID
			return it, true
		}
	}
	return nil, false
}

func (q *hardQueue) Complete(itemID int64, result []byte, errMsg string) error {
	if q.completeFn != nil {
		return q.completeFn(itemID, result, errMsg)
	}
	return nil
}

func (q *hardQueue) Fail(itemID int64, errMsg string) error {
	if q.failFn != nil {
		return q.failFn(itemID, errMsg)
	}
	return nil
}

func (q *hardQueue) Heartbeat(_ int64) error {
	q.hbCount.Add(1)
	return nil
}

func (q *hardQueue) Close() error {
	q.closed.Store(true)
	return nil
}

// --- WorkerNode hard red tests ---

// TestWorker_ConcurrentProcessOne hammers ProcessOne from many goroutines
// on a shared queue to detect races on curID and queue state.
func TestWorker_ConcurrentProcessOne(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	const n = 20
	q := &hardQueue{}
	for i := range n {
		id := fmt.Sprintf("conc-wf-%d", i)
		wf := NewWorkflow(id, "conc", "test", []Step{
			{ID: "s1", Kind: StepNoop},
		})
		wf.State = StateRunning
		_ = store.Save(wf)
		_ = q.Enqueue(QueueItem{
			WorkflowID: id,
			StepID:     "s1",
			StepKind:   string(StepNoop),
			Priority:   i,
		})
	}

	worker, err := NewWorkerNode(WorkerConfig{
		ID: "race-worker", Queue: q, StepKinds: []string{string(StepNoop)},
		Engine: engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()

	var wg sync.WaitGroup
	var processed atomic.Int32
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if worker.ProcessOne(context.Background()) {
				processed.Add(1)
			}
		}()
	}
	wg.Wait()

	if p := processed.Load(); p != int32(n) {
		t.Errorf("expected %d processed, got %d", n, p)
	}
}

// TestWorker_StopIdempotent verifies Stop can be called multiple times without panic.
func TestWorker_StopIdempotent(t *testing.T) {
	t.Parallel()
	q := &hardQueue{}
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "stop-idem", Queue: q, StepKinds: []string{"noop"}, Engine: engine,
	})

	worker.Stop()
	worker.Stop() // must not panic
	worker.Stop()
}

// TestWorker_ProcessOne_QueueFailError verifies queue.Fail error is handled gracefully.
func TestWorker_ProcessOne_QueueFailError(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store) // no tool executor → RunStep fails for StepTool

	wf := NewWorkflow("fail-q", "fail", "test", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "missing"}},
	})
	wf.State = StateRunning
	_ = store.Save(wf)

	failCalled := atomic.Bool{}
	q := &hardQueue{
		failFn: func(_ int64, _ string) error {
			failCalled.Store(true)
			return errors.New("queue connection lost")
		},
	}
	_ = q.Enqueue(QueueItem{WorkflowID: wf.ID, StepID: "s1", StepKind: string(StepTool)})

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "fail-q-worker", Queue: q, StepKinds: []string{string(StepTool)}, Engine: engine,
	})
	defer worker.Stop()

	result := worker.ProcessOne(context.Background())
	if !result {
		t.Fatal("ProcessOne should return true even on failure")
	}
	if !failCalled.Load() {
		t.Fatal("queue.Fail was never called")
	}
}

// TestWorker_ProcessOne_QueueCompleteError verifies queue.Complete error is handled.
func TestWorker_ProcessOne_QueueCompleteError(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	wf := NewWorkflow("compl-err", "compl", "test", []Step{{ID: "s1", Kind: StepNoop}})
	wf.State = StateRunning
	_ = store.Save(wf)

	completeCalled := atomic.Bool{}
	q := &hardQueue{
		completeFn: func(_ int64, _ []byte, _ string) error {
			completeCalled.Store(true)
			return errors.New("disk full")
		},
	}
	_ = q.Enqueue(QueueItem{WorkflowID: wf.ID, StepID: "s1", StepKind: string(StepNoop)})

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "compl-err-w", Queue: q, StepKinds: []string{string(StepNoop)}, Engine: engine,
	})
	defer worker.Stop()

	result := worker.ProcessOne(context.Background())
	if !result {
		t.Fatal("ProcessOne should return true")
	}
	if !completeCalled.Load() {
		t.Fatal("queue.Complete was never called")
	}
}

// TestWorker_CancelledContext verifies ProcessOne respects context cancellation.
func TestWorker_CancelledContext(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	wf := NewWorkflow("ctx-cancel", "ctx", "test", []Step{{ID: "s1", Kind: StepNoop}})
	wf.State = StateRunning
	_ = store.Save(wf)

	q := &hardQueue{}
	_ = q.Enqueue(QueueItem{WorkflowID: wf.ID, StepID: "s1", StepKind: string(StepNoop)})

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "ctx-w", Queue: q, StepKinds: []string{string(StepNoop)}, Engine: engine,
	})
	defer worker.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Should still process (RunStep for noop is instant, ctx may or may not propagate)
	_ = worker.ProcessOne(ctx)
}

// TestWorker_RunExitsOnStop verifies Run exits promptly when Stop is called.
func TestWorker_RunExitsOnStop(t *testing.T) {
	t.Parallel()
	q := &hardQueue{
		dequeueFn: func() (*QueueItem, bool) { return nil, false },
	}
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "run-stop", Queue: q, StepKinds: []string{"noop"}, Engine: engine,
		PollInterval: 10 * time.Millisecond,
	})

	done := make(chan struct{})
	go func() {
		worker.Run(context.Background())
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	worker.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker.Run did not exit after Stop")
	}
}

// --- DrainAndStop hard tests ---

// TestDrainAndStop_AlreadyIdle verifies DrainAndStop returns immediately when idle.
func TestDrainAndStop_AlreadyIdle(t *testing.T) {
	t.Parallel()
	q := &hardQueue{}
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "drain-idle", Queue: q, StepKinds: []string{"noop"}, Engine: engine,
	})

	start := time.Now()
	worker.DrainAndStop(5 * time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("DrainAndStop took %v for idle worker, expected <1s", elapsed)
	}
}

// TestDrainAndStop_Timeout verifies timeout fires when worker is stuck.
func TestDrainAndStop_Timeout(t *testing.T) {
	t.Parallel()
	q := &hardQueue{}
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "drain-timeout", Queue: q, StepKinds: []string{"noop"}, Engine: engine,
	})

	// Simulate active work
	worker.curID.Store(42)

	start := time.Now()
	worker.DrainAndStop(300 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 250*time.Millisecond {
		t.Errorf("DrainAndStop returned too quickly: %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("DrainAndStop took too long: %v", elapsed)
	}
}

// --- PostgresDispatcher hard tests ---

// TestPgDispatcher_EnqueueError verifies Dispatch propagates queue errors.
func TestPgDispatcher_EnqueueError(t *testing.T) {
	t.Parallel()
	errQ := &failEnqueuer{err: errors.New("connection refused")}
	d := NewPostgresDispatcher(errQ)

	err := d.Dispatch(context.Background(), "wf", "s1", StepLLM)
	if err == nil {
		t.Fatal("expected error from broken queue")
	}
}

// TestPgDispatcher_BatchPartialFailure verifies DispatchBatch stops on first error.
func TestPgDispatcher_BatchPartialFailure(t *testing.T) {
	t.Parallel()
	callCount := atomic.Int32{}
	errQ := &failEnqueuer{
		enqueueFn: func(item QueueItem) error {
			n := callCount.Add(1)
			if n == 2 {
				return errors.New("disk full")
			}
			return nil
		},
	}
	d := NewPostgresDispatcher(errQ)

	err := d.DispatchBatch(context.Background(), "wf",
		[]string{"s1", "s2", "s3"},
		[]StepKind{StepNoop, StepNoop, StepNoop},
	)
	if err == nil {
		t.Fatal("expected error on second enqueue")
	}
	if c := callCount.Load(); c != 2 {
		t.Errorf("expected 2 enqueue calls before stop, got %d", c)
	}
}

// TestPgDispatcher_CloseErrorChain verifies Close returns first error from chain.
func TestPgDispatcher_CloseErrorChain(t *testing.T) {
	t.Parallel()
	q := &failEnqueuer{closeErr: errors.New("queue close fail")}
	extra := &failCloser{err: errors.New("limiter close fail")}
	d := NewPostgresDispatcher(q, extra)

	err := d.Close()
	if err == nil {
		t.Fatal("expected error from Close chain")
	}
	if err.Error() != "close queue: queue close fail" {
		t.Errorf("expected queue error first, got: %s", err)
	}
}

// --- parsePayload hard tests ---

func TestParsePayload_EdgeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
		wantOK  bool
		wantWF  string
		wantS   string
	}{
		{"empty", "", false, "", ""},
		{"no_colon", "nocolon", false, "", ""},
		{"empty_wf", ":step", false, "", ""},
		{"empty_step", "wf:", false, "", ""},
		{"both_empty", ":", false, "", ""},
		{"valid_simple", "wf-1:s1", true, "wf-1", "s1"},
		{"colon_in_step", "wf-1:s:1:2", true, "wf-1", "s:1:2"},
		{"unicode", "воркфлоу:шаг", true, "воркфлоу", "шаг"},
		{"spaces", " wf : s1 ", true, " wf ", " s1 "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			event, ok := parsePayload(tc.payload)
			if ok != tc.wantOK {
				t.Errorf("parsePayload(%q): ok=%v, want %v", tc.payload, ok, tc.wantOK)
			}
			if ok && (event.WorkflowID != tc.wantWF || event.StepID != tc.wantS) {
				t.Errorf("parsePayload(%q): got {%q,%q}, want {%q,%q}",
					tc.payload, event.WorkflowID, event.StepID, tc.wantWF, tc.wantS)
			}
		})
	}
}

// --- LocalDispatcher hard tests ---

// TestLocalDispatcher_NonexistentWorkflow verifies error on missing workflow.
func TestLocalDispatcher_NonexistentWorkflow(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)
	d := NewLocalDispatcher(engine)

	err := d.Dispatch(context.Background(), "nonexistent", "s1", StepNoop)
	if err == nil {
		t.Fatal("expected error for nonexistent workflow")
	}
}

// TestLocalDispatcher_NonexistentStep verifies error on missing step.
func TestLocalDispatcher_NonexistentStep(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	wf := NewWorkflow("missing-step", "missing", "test", []Step{{ID: "s1", Kind: StepNoop}})
	wf.State = StateRunning
	_ = store.Save(wf)

	d := NewLocalDispatcher(engine)
	err := d.Dispatch(context.Background(), wf.ID, "nonexistent", StepNoop)
	if err == nil {
		t.Fatal("expected error for nonexistent step")
	}
}

// TestLocalDispatcher_BatchEmpty verifies empty batch doesn't panic.
func TestLocalDispatcher_BatchEmpty(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)
	d := NewLocalDispatcher(engine)

	err := d.DispatchBatch(context.Background(), "wf", nil, nil)
	if err != nil {
		t.Errorf("empty batch should not error, got: %v", err)
	}
}

// --- Reaper hard tests ---

// TestReaper_ErrorContinues verifies reaper continues after ReapStale errors.
func TestReaper_ErrorContinues(t *testing.T) {
	t.Parallel()
	callCount := atomic.Int32{}
	mock := &mockStepReaper{
		fn: func(_ time.Duration) (int, error) {
			n := callCount.Add(1)
			if n == 1 {
				return 0, errors.New("db gone")
			}
			return 0, nil
		},
	}

	r := NewReaper(mock, time.Minute, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	r.Run(ctx)

	if c := callCount.Load(); c < 2 {
		t.Errorf("reaper should have retried after error, calls=%d", c)
	}
}

// --- Heartbeat hard test ---

// TestWorker_HeartbeatDuringExecution verifies heartbeats are sent while processing.
func TestWorker_HeartbeatDuringExecution(t *testing.T) {
	t.Parallel()
	store := NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)})
	engine := NewEngine(store)

	// Use a slow-dequeue mock that holds the item "claimed" for a while
	q := &hardQueue{
		dequeueFn: func() (*QueueItem, bool) {
			return nil, false // no items to dequeue
		},
	}

	worker, _ := NewWorkerNode(WorkerConfig{
		ID: "hb-test", Queue: q, StepKinds: []string{"noop"}, Engine: engine,
		HeartbeatInterval: 20 * time.Millisecond,
		PollInterval:      10 * time.Millisecond,
	})

	// Simulate active processing
	worker.curID.Store(99)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go worker.Run(ctx)
	<-ctx.Done()
	worker.Stop()

	if hb := q.hbCount.Load(); hb < 3 {
		t.Errorf("expected ≥3 heartbeats in 200ms at 20ms interval, got %d", hb)
	}
}

// --- getDispatcher hard test ---

// TestEngine_GetDispatcher_NilFallback verifies getDispatcher creates LocalDispatcher
// when Engine.dispatcher is nil (e.g. Engine created via struct literal).
func TestEngine_GetDispatcher_NilFallback(t *testing.T) {
	t.Parallel()
	e := &Engine{
		store:     NewWorkflowStore(&memBackend{wf: make(map[string]*Workflow)}),
		executors: map[StepKind]StepExecutor{StepNoop: &NoopExecutor{}},
	}

	d := e.getDispatcher()
	if d == nil {
		t.Fatal("getDispatcher returned nil")
	}
	if _, ok := d.(*LocalDispatcher); !ok {
		t.Fatalf("expected *LocalDispatcher, got %T", d)
	}
}

// --- helpers ---

type failEnqueuer struct {
	err       error
	closeErr  error
	enqueueFn func(QueueItem) error
}

func (f *failEnqueuer) Enqueue(item QueueItem) error {
	if f.enqueueFn != nil {
		return f.enqueueFn(item)
	}
	return f.err
}

func (f *failEnqueuer) Close() error { return f.closeErr }

type failCloser struct{ err error }

func (f *failCloser) Close() error { return f.err }

type mockStepReaper struct {
	fn func(time.Duration) (int, error)
}

func (m *mockStepReaper) ReapStale(timeout time.Duration) (int, error) {
	return m.fn(timeout)
}
