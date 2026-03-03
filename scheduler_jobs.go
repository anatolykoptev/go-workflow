package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	schedulerDirPerm  fs.FileMode = 0o750
	schedulerFilePerm fs.FileMode = 0o600
)

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
