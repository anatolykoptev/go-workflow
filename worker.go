package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	defaultHeartbeatInterval = 30 * time.Second
	defaultPollInterval      = time.Second
)

// StepWorkerQueue is the queue interface consumed by WorkerNode.
// Implemented by store.StepQueue.
type StepWorkerQueue interface {
	Dequeue(workerID string, kinds []string) (*QueueItem, bool)
	Complete(itemID int64, result []byte, errMsg string) error
	Fail(itemID int64, errMsg string) error
	Heartbeat(itemID int64) error
	io.Closer
}

// WorkerConfig configures a WorkerNode.
type WorkerConfig struct {
	ID                string          // unique worker identifier
	Queue             StepWorkerQueue // queue to dequeue from
	StepKinds         []string        // which step kinds this worker handles
	Engine            *Engine         // engine with executors for step execution
	HeartbeatInterval time.Duration   // default 30s
	PollInterval      time.Duration   // default 1s
	Logger            *slog.Logger
}

// WorkerNode dequeues steps from step_queue and executes them via the Engine.
type WorkerNode struct {
	id      string
	kinds   []string
	engine  *Engine
	queue   StepWorkerQueue
	hbInt   time.Duration
	pollInt time.Duration
	logger  *slog.Logger
	stop    chan struct{}
	curID   atomic.Int64 // currently processing item ID; 0 = idle
}

// NewWorkerNode creates a worker that polls the step_queue and executes steps.
func NewWorkerNode(cfg WorkerConfig) (*WorkerNode, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("worker %s: engine is required", cfg.ID)
	}
	if cfg.Queue == nil {
		return nil, fmt.Errorf("worker %s: queue is required", cfg.ID)
	}
	if len(cfg.StepKinds) == 0 {
		return nil, fmt.Errorf("worker %s: at least one step kind required", cfg.ID)
	}
	hb, poll := cfg.HeartbeatInterval, cfg.PollInterval
	if hb == 0 {
		hb = defaultHeartbeatInterval
	}
	if poll == 0 {
		poll = defaultPollInterval
	}
	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}
	return &WorkerNode{
		id: cfg.ID, kinds: cfg.StepKinds, engine: cfg.Engine,
		queue: cfg.Queue, hbInt: hb, pollInt: poll,
		logger: lg, stop: make(chan struct{}),
	}, nil
}

// Run starts the main dequeue-execute loop and a heartbeat goroutine.
// Blocks until ctx is cancelled or Stop is called.
func (w *WorkerNode) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.heartbeatLoop(ctx)
	w.logger.Info("worker started", "worker", w.id, "kinds", w.kinds)

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("worker stopping", "worker", w.id, "reason", "context")
			return
		case <-w.stop:
			w.logger.Info("worker stopping", "worker", w.id, "reason", "stop")
			return
		default:
		}
		if !w.ProcessOne(ctx) {
			select {
			case <-time.After(w.pollInt):
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			}
		}
	}
}

// ProcessOne dequeues one item, executes it, and completes/fails in the queue.
// Returns false if the queue was empty.
func (w *WorkerNode) ProcessOne(ctx context.Context) bool {
	item, ok := w.queue.Dequeue(w.id, w.kinds)
	if !ok {
		return false
	}
	w.curID.Store(item.ID)
	defer w.curID.Store(0)

	w.logger.Info("step dequeued",
		"worker", w.id, "workflow", item.WorkflowID,
		"step", item.StepID, "kind", item.StepKind,
	)
	if err := w.engine.RunStep(ctx, item.WorkflowID, item.StepID); err != nil {
		w.logger.Error("step failed",
			"worker", w.id, "workflow", item.WorkflowID,
			"step", item.StepID, "error", err.Error(),
		)
		if qErr := w.queue.Fail(item.ID, err.Error()); qErr != nil {
			w.logger.Error("queue fail", "worker", w.id, "error", qErr.Error())
		}
		return true
	}
	result := w.readStepResult(item.WorkflowID, item.StepID)
	if err := w.queue.Complete(item.ID, result, ""); err != nil {
		w.logger.Error("queue complete", "worker", w.id, "error", err.Error())
	}
	w.logger.Info("step completed",
		"worker", w.id, "workflow", item.WorkflowID, "step", item.StepID,
	)
	return true
}

// readStepResult loads the workflow and marshals the step result to JSON.
func (w *WorkerNode) readStepResult(workflowID, stepID string) []byte {
	wf, ok := w.engine.Store().Load(workflowID)
	if !ok {
		return nil
	}
	step := wf.GetStep(stepID)
	if step == nil || step.Result == nil {
		return nil
	}
	data, err := json.Marshal(step.Result)
	if err != nil {
		return nil
	}
	return data
}

// heartbeatLoop sends periodic heartbeats for the currently processing item.
func (w *WorkerNode) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(w.hbInt)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
			if id := w.curID.Load(); id != 0 {
				if err := w.queue.Heartbeat(id); err != nil {
					w.logger.Error("heartbeat", "worker", w.id, "item", id, "error", err.Error())
				}
			}
		}
	}
}

// Stop signals the worker to shut down and closes the queue connection.
func (w *WorkerNode) Stop() {
	select {
	case <-w.stop:
		return
	default:
		close(w.stop)
	}
	if err := w.queue.Close(); err != nil {
		w.logger.Error("queue close", "worker", w.id, "error", err.Error())
	}
}
