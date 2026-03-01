package workflow

import (
	"path/filepath"
	"testing"
	"time"
)

func TestScheduler_AddJob_Every(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	every := int64(5000)
	job, err := s.AddJob("test", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "message"})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("NextRunAtMS should be set")
	}
	if !job.Enabled {
		t.Error("job should be enabled")
	}
}

func TestScheduler_AddJob_At(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := s.AddJob("once", Schedule{Kind: "at", AtMS: &at}, JobPayload{Kind: "workflow"})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if *job.State.NextRunAtMS != at {
		t.Errorf("NextRunAtMS = %d, want %d", *job.State.NextRunAtMS, at)
	}
}

func TestScheduler_AddJob_Cron(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	job, err := s.AddJob("daily", Schedule{Kind: "cron", Expr: "0 9 * * *"}, JobPayload{Kind: "workflow"})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("NextRunAtMS should be set for cron")
	}
}

func TestScheduler_AddJob_InvalidSchedule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	_, err := s.AddJob("bad", Schedule{Kind: "unknown"}, JobPayload{Kind: "x"})
	if err == nil {
		t.Error("expected error for invalid schedule")
	}
}

func TestScheduler_RemoveJob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	every := int64(1000)
	job, _ := s.AddJob("rm", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "message"})
	if !s.RemoveJob(job.ID) {
		t.Error("RemoveJob should return true")
	}
	if s.RemoveJob(job.ID) {
		t.Error("second RemoveJob should return false")
	}
}

func TestScheduler_EnableDisable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	every := int64(1000)
	job, _ := s.AddJob("toggle", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "message"})

	updated := s.EnableJob(job.ID, false)
	if updated == nil {
		t.Fatal("EnableJob returned nil")
	}
	if updated.Enabled {
		t.Error("job should be disabled")
	}
	if updated.State.NextRunAtMS != nil {
		t.Error("NextRunAtMS should be nil when disabled")
	}

	updated = s.EnableJob(job.ID, true)
	if !updated.Enabled {
		t.Error("job should be re-enabled")
	}
}

func TestScheduler_ListJobs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	every := int64(1000)
	_, _ = s.AddJob("a", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "message"})
	job2, _ := s.AddJob("b", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "message"})
	s.EnableJob(job2.ID, false)

	all := s.ListJobs(true)
	if len(all) != 2 {
		t.Errorf("ListJobs(true) = %d, want 2", len(all))
	}

	enabled := s.ListJobs(false)
	if len(enabled) != 1 {
		t.Errorf("ListJobs(false) = %d, want 1", len(enabled))
	}
}

func TestScheduler_PersistReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")

	every := int64(60000)
	s1 := NewScheduler(path, nil)
	_, _ = s1.AddJob("persist", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "workflow", WorkflowID: "wf-1"})

	s2 := NewScheduler(path, nil)
	jobs := s2.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("reloaded %d jobs, want 1", len(jobs))
	}
	if jobs[0].Name != "persist" {
		t.Errorf("name = %q", jobs[0].Name)
	}
	if jobs[0].Payload.WorkflowID != "wf-1" {
		t.Errorf("workflow_id = %q", jobs[0].Payload.WorkflowID)
	}
}

func TestScheduler_Status(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewScheduler(filepath.Join(dir, "jobs.json"), nil)

	status := s.Status()
	if status["running"] != false {
		t.Error("should not be running")
	}
}

func TestScheduler_JobExecution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	executed := make(chan string, 1)
	handler := func(job *Job) (string, error) {
		executed <- job.ID
		return "ok", nil
	}

	s := NewScheduler(filepath.Join(dir, "jobs.json"), handler)

	every := int64(100)
	job, _ := s.AddJob("fast", Schedule{Kind: "every", EveryMS: &every}, JobPayload{Kind: "test"})

	// Manually set NextRunAtMS to past and mark running so checkJobs fires.
	s.mu.Lock()
	s.running = true
	past := time.Now().Add(-time.Second).UnixMilli()
	for i := range s.store.Jobs {
		if s.store.Jobs[i].ID == job.ID {
			s.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	s.mu.Unlock()

	s.checkJobs()

	select {
	case id := <-executed:
		if id != job.ID {
			t.Errorf("executed job %q, want %q", id, job.ID)
		}
	case <-time.After(time.Second):
		t.Error("job handler was not called within timeout")
	}
}

func TestComputeNextRun_Every(t *testing.T) {
	t.Parallel()
	every := int64(5000)
	now := int64(1000000)
	schedule := &Schedule{Kind: "every", EveryMS: &every}
	next := computeNextRun(schedule, now)
	if next == nil || *next != now+every {
		t.Errorf("got %v, want %d", next, now+every)
	}
}

func TestComputeNextRun_At_Future(t *testing.T) {
	t.Parallel()
	at := int64(2000000)
	schedule := &Schedule{Kind: "at", AtMS: &at}
	next := computeNextRun(schedule, 1000000)
	if next == nil || *next != at {
		t.Errorf("got %v, want %d", next, at)
	}
}

func TestComputeNextRun_At_Past(t *testing.T) {
	t.Parallel()
	at := int64(500000)
	schedule := &Schedule{Kind: "at", AtMS: &at}
	next := computeNextRun(schedule, 1000000)
	if next != nil {
		t.Error("past 'at' should return nil")
	}
}

func TestComputeNextRun_Unknown(t *testing.T) {
	t.Parallel()
	schedule := &Schedule{Kind: "bad"}
	next := computeNextRun(schedule, 0)
	if next != nil {
		t.Error("unknown kind should return nil")
	}
}
