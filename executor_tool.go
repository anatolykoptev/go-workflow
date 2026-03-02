package workflow

import (
	"context"
	"fmt"
)

// ToolRunner is the interface for executing tools (satisfied by tools.ToolRegistry).
type ToolRunner interface {
	Execute(ctx context.Context, name string, args map[string]any) (string, error)
}

// ToolExecutor calls a registered tool and stores the result in workflow context.
type ToolExecutor struct {
	runner ToolRunner
}

func NewToolExecutor(runner ToolRunner) *ToolExecutor {
	return &ToolExecutor{runner: runner}
}

func (e *ToolExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	toolName, _ := step.Config["tool"].(string)
	if toolName == "" {
		return fmt.Errorf("step %s: missing 'tool' in config", step.ID)
	}

	args := make(map[string]any)
	if a, ok := step.Config["args"].(map[string]any); ok {
		// Resolve context references: "$steps.{id}.result" -> actual value
		for k, v := range a {
			args[k] = resolveRef(v, wf)
		}
	}

	result, err := e.runner.Execute(ctx, toolName, args)
	if err != nil {
		return fmt.Errorf("tool %s: %w", toolName, err)
	}

	step.Result = result
	wf.Context[step.ID] = result
	return nil
}
