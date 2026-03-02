package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SuspendExecutor pauses the workflow until a specified time.
// Config: {"suspend_until_ms": <unix_ms>}
// The watchdog auto-resumes workflows past their deadline.
type SuspendExecutor struct{}

// NewSuspendExecutor creates a new SuspendExecutor.
func NewSuspendExecutor() *SuspendExecutor {
	return &SuspendExecutor{}
}

func (e *SuspendExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	untilMS, _ := step.Config["suspend_until_ms"].(float64)
	if untilMS <= 0 {
		return fmt.Errorf("suspend step %s: missing or invalid 'suspend_until_ms'", step.ID)
	}

	now := time.Now().UnixMilli()
	if int64(untilMS) <= now {
		step.Result = "deadline already passed"
		wf.Context[step.ID] = "resumed"
		return nil
	}

	step.Result = fmt.Sprintf("suspended until %d", int64(untilMS))
	wf.Context[step.ID+"_suspend_until_ms"] = int64(untilMS)
	return errSuspendRequested
}

var errSuspendRequested = errors.New("suspend requested")
