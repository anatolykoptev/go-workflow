package workflow

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Schedule defines when a job runs.
type Schedule struct {
	Kind    string `json:"kind"`               // "at", "every", "cron"
	AtMS    *int64 `json:"at_ms,omitempty"`    // for kind="at": one-shot unix ms
	EveryMS *int64 `json:"every_ms,omitempty"` // for kind="every": interval ms
	Expr    string `json:"expr,omitempty"`     // for kind="cron": 5-field expression
	TZ      string `json:"tz,omitempty"`       // timezone for cron (e.g. "Europe/Moscow")
}

// JobPayload defines what happens when a job fires.
type JobPayload struct {
	Kind       string         `json:"kind"` // "workflow", "message", or custom
	WorkflowID string         `json:"workflow_id,omitempty"`
	TemplateID string         `json:"template_id,omitempty"`
	Message    string         `json:"message,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
}

// JobState tracks execution state of a scheduled job.
type JobState struct {
	NextRunAtMS *int64 `json:"next_run_at_ms,omitempty"`
	LastRunAtMS *int64 `json:"last_run_at_ms,omitempty"`
	LastStatus  string `json:"last_status,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// Job is a scheduled task.
type Job struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Enabled        bool       `json:"enabled"`
	Schedule       Schedule   `json:"schedule"`
	Payload        JobPayload `json:"payload"`
	State          JobState   `json:"state"`
	CreatedAtMS    int64      `json:"created_at_ms"`
	UpdatedAtMS    int64      `json:"updated_at_ms"`
	DeleteAfterRun bool       `json:"delete_after_run"`
}

// JobHandler is called when a scheduled job is due.
type JobHandler func(job *Job) (string, error)

type jobStore struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

// Scheduler manages time-based job scheduling with JSON file persistence.
type Scheduler struct {
	storePath string
	store     *jobStore
	onJob     JobHandler
	logger    *slog.Logger
	metrics   *Metrics
	mu        sync.RWMutex
	running   bool
	stopChan  chan struct{}
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// NewScheduler creates a scheduler backed by the given JSON file path.
func NewScheduler(storePath string, handler JobHandler, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		storePath: storePath,
		onJob:     handler,
		metrics:   GlobalMetrics,
		stopChan:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	_ = s.loadStore()
	return s
}

// WithSchedulerLogger sets the logger for the scheduler.
func WithSchedulerLogger(l *slog.Logger) SchedulerOption {
	return func(s *Scheduler) { s.logger = l }
}

// WithSchedulerMetrics sets the metrics instance for the scheduler.
func WithSchedulerMetrics(m *Metrics) SchedulerOption {
	return func(s *Scheduler) { s.metrics = m }
}

func (s *Scheduler) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Start begins the scheduler tick loop.
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	if err := s.loadStore(); err != nil {
		return fmt.Errorf("scheduler load: %w", err)
	}

	s.recomputeNextRuns()
	if err := s.saveStore(); err != nil {
		return fmt.Errorf("scheduler save: %w", err)
	}

	s.running = true
	go s.runLoop()
	return nil
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}
	s.running = false
	close(s.stopChan)
}

func (s *Scheduler) runLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.checkJobs()
		}
	}
}

func (s *Scheduler) checkJobs() {
	s.mu.RLock()
	if !s.running {
		s.mu.RUnlock()
		return
	}

	now := time.Now().UnixMilli()
	var dueIDs []string
	for i := range s.store.Jobs {
		job := &s.store.Jobs[i]
		if job.Enabled && job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
			dueIDs = append(dueIDs, job.ID)
		}
	}
	s.mu.RUnlock()

	for _, id := range dueIDs {
		s.executeJobByID(id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveStore(); err != nil {
		s.log().Error("scheduler: save failed", slog.String("error", err.Error()))
	}
}

func (s *Scheduler) executeJobByID(id string) {
	s.mu.RLock()
	var jobCopy Job
	var found bool
	for _, j := range s.store.Jobs {
		if j.ID == id {
			jobCopy = j
			found = true
			break
		}
	}
	s.mu.RUnlock()

	if !found {
		return
	}

	s.metrics.SchedulerJobsExecuted.Add(1)
	startTime := time.Now().UnixMilli()

	var err error
	if s.onJob != nil {
		_, err = s.onJob(&jobCopy)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.updateJobAfterExecution(id, startTime, err)
}

func (s *Scheduler) updateJobAfterExecution(id string, startTime int64, err error) {
	for i := range s.store.Jobs {
		job := &s.store.Jobs[i]
		if job.ID != id {
			continue
		}

		job.State.LastRunAtMS = &startTime
		job.UpdatedAtMS = time.Now().UnixMilli()

		if err != nil {
			job.State.LastStatus = "error"
			job.State.LastError = err.Error()
			s.metrics.SchedulerJobsFailed.Add(1)
		} else {
			job.State.LastStatus = "ok"
			job.State.LastError = ""
		}

		if job.Schedule.Kind == "at" {
			if job.DeleteAfterRun {
				s.removeJobUnsafe(job.ID)
			} else {
				job.Enabled = false
				job.State.NextRunAtMS = nil
			}
		} else {
			nextRun := computeNextRun(&job.Schedule, time.Now().UnixMilli())
			job.State.NextRunAtMS = nextRun
		}
		return
	}
}
