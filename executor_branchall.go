package workflow

import (
	"context"
	"fmt"
)

// BranchAllExecutor fans out N branches in parallel.
// Config:
//
//	{
//	  "branches": [
//	    {"id": "b1", "kind": "tool", "config": {"tool": "fetch_news"}},
//	    {"id": "b2", "kind": "llm",  "config": {"prompt": "..."}}
//	  ]
//	}
//
// Each branch becomes an independent child step. All run in parallel.
// Results are collected into `wf.Context[parentID]` as `map[branchID]result`.
type BranchAllExecutor struct {
	engine *Engine
}

// NewBranchAllExecutor creates a BranchAll executor.
func NewBranchAllExecutor(engine *Engine) *BranchAllExecutor {
	return &BranchAllExecutor{engine: engine}
}

func (e *BranchAllExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	rawBranches, ok := step.Config["branches"]
	if !ok {
		return fmt.Errorf("branchall step %s: missing 'branches' config", step.ID)
	}

	branches, ok := rawBranches.([]any)
	if !ok {
		return fmt.Errorf("branchall step %s: 'branches' must be an array", step.ID)
	}

	if len(branches) == 0 {
		step.Result = "no branches"
		wf.Context[step.ID] = map[string]any{}
		return nil
	}

	var children []Step
	for i, raw := range branches {
		branchMap, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("branchall step %s: branch %d is not an object", step.ID, i)
		}

		branchID, _ := branchMap["id"].(string)
		if branchID == "" {
			branchID = fmt.Sprintf("%s_b%d", step.ID, i)
		}

		kind := StepKind(stringFromConfig(branchMap, "kind"))
		if kind == "" {
			kind = StepTool
		}

		cfg, _ := branchMap["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}

		children = append(children, Step{
			ID:     branchID,
			Kind:   kind,
			Config: deepCloneMap(cfg),
			State:  StepPending,
		})
	}

	joinID := step.ID + "_join"
	joinStep := Step{
		ID:        joinID,
		Kind:      StepNoop,
		Config:    map[string]any{},
		State:     StepPending,
		DependsOn: make([]string, 0),
	}

	for _, child := range children {
		joinStep.DependsOn = append(joinStep.DependsOn, child.ID)
	}

	children = append(children, joinStep)

	step.Result = fmt.Sprintf("expanded %d branches", len(branches))
	wf.Context[step.ID+"_count"] = len(branches)

	return e.engine.InjectStepsAndRewriteDeps(wf.ID, children, step.ID, joinID)
}
