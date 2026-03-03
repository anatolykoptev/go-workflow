package workflow

import (
	"context"
	"fmt"
)

// ForEachExecutor iterates over a list and injects child steps for each item.
// Config:
//
//	{
//	  "items": "step_id_that_produced_list",
//	  "step_kind": "tool",
//	  "step_config": {"tool": "process_item"},
//	  "sequential": false,
//	  "concurrency": 0
//	}
//
// Each child step gets `{"item": <value>, "index": <int>}` merged into its config.
// Results are collected into `wf.Context[parentID]` as `[]any`.
type ForEachExecutor struct {
	engine *Engine
}

// NewForEachExecutor creates a ForEach executor.
func NewForEachExecutor(engine *Engine) *ForEachExecutor {
	return &ForEachExecutor{engine: engine}
}

func (e *ForEachExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	itemsRef, _ := step.Config["items"].(string)
	if itemsRef == "" {
		return fmt.Errorf("foreach step %s: missing 'items' config", step.ID)
	}

	rawItems, ok := wf.Context[itemsRef]
	if !ok {
		return fmt.Errorf("foreach step %s: context key %q not found", step.ID, itemsRef)
	}

	items, ok := rawItems.([]any)
	if !ok {
		return fmt.Errorf("foreach step %s: context[%q] is not a list", step.ID, itemsRef)
	}

	if len(items) == 0 {
		step.Result = "[]"
		wf.Context[step.ID] = []any{}
		return nil
	}

	children, joinID := buildForEachChildren(step, items)

	step.Result = fmt.Sprintf("expanded %d items", len(items))
	wf.Context[step.ID+"_count"] = len(items)

	return e.engine.InjectStepsAndRewriteDeps(wf.ID, children, step.ID, joinID)
}

func buildForEachChildren(step *Step, items []any) ([]Step, string) {
	childKind := StepKind(stringFromConfig(step.Config, "step_kind"))
	if childKind == "" {
		childKind = StepTool
	}

	childConfig, _ := step.Config["step_config"].(map[string]any)
	if childConfig == nil {
		childConfig = map[string]any{}
	}

	sequential, _ := step.Config["sequential"].(bool)
	limitF, _ := step.Config["concurrency"].(float64)
	limit := int(limitF)

	var children []Step
	for i, item := range items {
		cfg := deepCloneMap(childConfig)
		cfg["item"] = item
		cfg["index"] = i

		childID := fmt.Sprintf("%s_%d", step.ID, i)
		child := Step{
			ID:     childID,
			Kind:   childKind,
			Config: cfg,
			State:  StepPending,
		}

		if sequential && i > 0 {
			child.DependsOn = []string{fmt.Sprintf("%s_%d", step.ID, i-1)}
		} else if !sequential && limit > 0 && i >= limit {
			child.DependsOn = []string{fmt.Sprintf("%s_%d", step.ID, i-limit)}
		}

		children = append(children, child)
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
		if !sequential {
			joinStep.DependsOn = append(joinStep.DependsOn, child.ID)
		}
	}
	if sequential && len(children) > 0 {
		joinStep.DependsOn = append(joinStep.DependsOn, children[len(children)-1].ID)
	}

	return append(children, joinStep), joinID
}

func stringFromConfig(cfg map[string]any, key string) string {
	v, _ := cfg[key].(string)
	return v
}
