package workflow

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ExecutionTrace is a reconstructed execution from event log.
type ExecutionTrace struct {
	WorkflowID string      `json:"workflow_id"`
	Steps      []StepTrace `json:"steps"`
	TotalMS    int64       `json:"total_ms"`
	Error      string      `json:"error,omitempty"`
	StartedAt  int64       `json:"started_at"`
	EndedAt    int64       `json:"ended_at"`
}

// StepTrace is a single step in the execution trace.
type StepTrace struct {
	StepID     string `json:"step_id"`
	StepKind   string `json:"step_kind"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    int64  `json:"ended_at"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Retries    int    `json:"retries,omitempty"`
}

// LoadEventLog reads a JSONL event log file and returns all events.
func LoadEventLog(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, scanner.Err()
}

// replayWorkflowEvent applies a workflow-level event to the trace.
func replayWorkflowEvent(trace *ExecutionTrace, e Event) {
	switch e.Type {
	case EventWFStarted:
		trace.StartedAt = e.Timestamp
	case EventWFCompleted:
		trace.EndedAt = e.Timestamp
	case EventWFFailed:
		trace.EndedAt = e.Timestamp
		trace.Error = e.Error
	}
}

// replayStepEvent applies a step-level event to the step map.
func replayStepEvent(steps map[string]*StepTrace, e Event) {
	switch e.Type {
	case EventStepStarted:
		steps[e.StepID] = &StepTrace{
			StepID:    e.StepID,
			StepKind:  e.StepKind,
			StartedAt: e.Timestamp,
		}
	case EventStepFinished:
		if st, ok := steps[e.StepID]; ok {
			st.EndedAt = e.Timestamp
			st.DurationMS = e.DurationMS
		}
	case EventStepFailed:
		if st, ok := steps[e.StepID]; ok {
			st.EndedAt = e.Timestamp
			st.DurationMS = e.DurationMS
			st.Error = e.Error
		}
	case EventStepRetried:
		if st, ok := steps[e.StepID]; ok {
			st.Retries++
		}
	}
}

// ReplayTrace reconstructs an execution trace from a sequence of events.
func ReplayTrace(events []Event) *ExecutionTrace {
	if len(events) == 0 {
		return &ExecutionTrace{}
	}

	trace := &ExecutionTrace{WorkflowID: events[0].WorkflowID}
	steps := make(map[string]*StepTrace)

	for _, e := range events {
		if trace.WorkflowID == "" {
			trace.WorkflowID = e.WorkflowID
		}
		replayWorkflowEvent(trace, e)
		replayStepEvent(steps, e)
	}

	for _, st := range steps {
		trace.Steps = append(trace.Steps, *st)
	}
	sort.Slice(trace.Steps, func(i, j int) bool {
		return trace.Steps[i].StartedAt < trace.Steps[j].StartedAt
	})

	if trace.StartedAt > 0 && trace.EndedAt > 0 {
		trace.TotalMS = trace.EndedAt - trace.StartedAt
	}

	return trace
}
