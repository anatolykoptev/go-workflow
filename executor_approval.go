package workflow

import (
	"context"
	"errors"
)

// ApprovalExecutor transitions the workflow to waiting_approval state.
// Actual approval handling is done externally via Engine.HandleApproval.
type ApprovalExecutor struct{}

func NewApprovalExecutor() *ApprovalExecutor {
	return &ApprovalExecutor{}
}

func (e *ApprovalExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	// Signal that this workflow needs approval.
	// The engine will catch this and pause the workflow.
	return errApprovalRequired
}

// errApprovalRequired is a sentinel error used by ApprovalExecutor to signal the engine.
var errApprovalRequired = errors.New("approval required")

// NoopExecutor completes immediately with 'ok' result. Useful as a join step.
type NoopExecutor struct{}

func (e *NoopExecutor) Execute(_ context.Context, step *Step, _ *Workflow) error {
	step.Result = "ok"
	return nil
}
