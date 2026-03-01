package workflow

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	eventLogDirPerm  fs.FileMode = 0o750
	eventLogFilePerm fs.FileMode = 0o600
)

// EventType identifies the kind of event in the log.
type EventType string

const (
	EventStepStarted      EventType = "step_started"
	EventStepFinished     EventType = "step_completed"
	EventStepFailed       EventType = "step_failed"
	EventStepRetried      EventType = "step_retried"
	EventWFStarted        EventType = "workflow_started"
	EventWFCompleted      EventType = "workflow_completed"
	EventWFFailed         EventType = "workflow_failed"
)

// Event is a single entry in the structured event log.
type Event struct {
	Type       EventType      `json:"type"`
	WorkflowID string         `json:"workflow_id"`
	StepID     string         `json:"step_id,omitempty"`
	StepKind   string         `json:"step_kind,omitempty"`
	Timestamp  int64          `json:"ts"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Error      string         `json:"error,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// EventLog is an append-only JSONL writer for workflow execution events.
type EventLog struct {
	dir string
	mu  sync.Mutex
}

// NewEventLog creates an event log backed by JSONL files in the given directory.
func NewEventLog(dir string) (*EventLog, error) {
	if err := os.MkdirAll(dir, eventLogDirPerm); err != nil {
		return nil, fmt.Errorf("eventlog dir: %w", err)
	}
	return &EventLog{dir: dir}, nil
}

// Append writes an event to the workflow's JSONL file.
func (el *EventLog) Append(e Event) error {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	el.mu.Lock()
	defer el.mu.Unlock()

	path := filepath.Join(el.dir, e.WorkflowID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, eventLogFilePerm)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// Path returns the JSONL file path for a given workflow.
func (el *EventLog) Path(workflowID string) string {
	return filepath.Join(el.dir, workflowID+".jsonl")
}
