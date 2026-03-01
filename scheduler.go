package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	schedulerDirPerm  fs.FileMode = 0o750
	schedulerFilePerm fs.FileMode = 0o600
)

// Schedule defines when a job runs.
type Schedule struct {
	Kind    string `json:"kind"`               // "at", "every", "cron"
	AtMS    *int64 `json:"at_ms,omitempty"`     // for kind="at": one-shot unix ms
	EveryMS *int64 `json:"every_ms,omitempty"`  // for kind="every": interval ms
	Expr    string `json:"expr,omitempty"`      // for kind="cron": 5-field expression
	TZ      string `json:"tz,omitempty"`        // timezone for cron (e.g. "Europe/Moscow")
}

// JobPayload defines what happens when a job fires.
type JobPayload struct {
	Kind       string         `json:"kind"`                    // "workflow", "message", or custom
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
	mu        sync.RWMutex
	running   bool
	stopChan  chan struct{}
}

// NewScheduler creates a scheduler backed by the given JSON file path.
func NewScheduler(storePath string, handler JobHandler, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		storePath: storePath,
		onJob:     handler,
		stopChan:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	_ = s.loadStore()
	return s
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithSchedulerLogger sets the logger for the scheduler.
func WithSchedulerLogger(l *slog.Logger) SchedulerOption {
	return func(s *Scheduler) { s.logger = l }
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

	GlobalMetrics.SchedulerJobsExecuted.Add(1)
	startTime := time.Now().UnixMilli()

	var err error
	if s.onJob != nil {
		_, err = s.onJob(&jobCopy)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
			GlobalMetrics.SchedulerJobsFailed.Add(1)
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

func computeNextRun(schedule *Schedule, nowMS int64) *int64 {
	switch schedule.Kind {
	case "at":
		if schedule.AtMS != nil && *schedule.AtMS > nowMS {
			return schedule.AtMS
		}
		return nil
	case "every":
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return nil
		}
		next := nowMS + *schedule.EveryMS
		return &next
	case "cron":
		next, err := NextCronRun(schedule.Expr, schedule.TZ, nowMS)
		if err != nil {
			return nil
		}
		return &next
	default:
		return nil
	}
}

func (s *Scheduler) recomputeNextRuns() {
	now := time.Now().UnixMilli()
	for i := range s.store.Jobs {
		job := &s.store.Jobs[i]
		if job.Enabled {
			job.State.NextRunAtMS = computeNextRun(&job.Schedule, now)
		}
	}
}

// AddJob adds a new scheduled job.
func (s *Scheduler) AddJob(name string, schedule Schedule, payload JobPayload) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	nextRun := computeNextRun(&schedule, now)
	if nextRun == nil {
		return nil, fmt.Errorf("invalid schedule: kind=%s expr=%q", schedule.Kind, schedule.Expr)
	}

	job := Job{
		ID:       strconv.FormatInt(time.Now().UnixNano(), 10),
		Name:     name,
		Enabled:  true,
		Schedule: schedule,
		Payload:  payload,
		State: JobState{
			NextRunAtMS: nextRun,
		},
		CreatedAtMS: now,
		UpdatedAtMS: now,
	}

	s.store.Jobs = append(s.store.Jobs, job)
	if err := s.saveStore(); err != nil {
		return nil, err
	}
	return &job, nil
}

// RemoveJob removes a job by ID.
func (s *Scheduler) RemoveJob(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeJobUnsafe(id)
}

func (s *Scheduler) removeJobUnsafe(id string) bool {
	before := len(s.store.Jobs)
	var jobs []Job
	for _, job := range s.store.Jobs {
		if job.ID != id {
			jobs = append(jobs, job)
		}
	}
	s.store.Jobs = jobs
	removed := len(s.store.Jobs) < before
	if removed {
		_ = s.saveStore()
	}
	return removed
}

// EnableJob enables or disables a job.
func (s *Scheduler) EnableJob(id string, enabled bool) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.store.Jobs {
		job := &s.store.Jobs[i]
		if job.ID == id {
			job.Enabled = enabled
			job.UpdatedAtMS = time.Now().UnixMilli()
			if enabled {
				job.State.NextRunAtMS = computeNextRun(&job.Schedule, time.Now().UnixMilli())
			} else {
				job.State.NextRunAtMS = nil
			}
			_ = s.saveStore()
			return job
		}
	}
	return nil
}

// ListJobs returns all jobs. If includeDisabled is false, only enabled jobs.
func (s *Scheduler) ListJobs(includeDisabled bool) []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if includeDisabled {
		out := make([]Job, len(s.store.Jobs))
		copy(out, s.store.Jobs)
		return out
	}

	var enabled []Job
	for _, job := range s.store.Jobs {
		if job.Enabled {
			enabled = append(enabled, job)
		}
	}
	return enabled
}

// Status returns scheduler status summary.
func (s *Scheduler) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"running": s.running,
		"jobs":    len(s.store.Jobs),
	}
}

func (s *Scheduler) loadStore() error {
	s.store = &jobStore{Version: 1, Jobs: []Job{}}

	data, err := os.ReadFile(s.storePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, s.store)
}

func (s *Scheduler) saveStore() error {
	dir := filepath.Dir(s.storePath)
	if err := os.MkdirAll(dir, schedulerDirPerm); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.store, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := s.storePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, schedulerFilePerm); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.storePath)
}
