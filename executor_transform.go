package workflow

import "context"

// TransformExecutor performs lightweight JSON data transformations.
// Config operations:
//   - "set": map of key->value pairs to set (supports {{stepID}} refs)
//   - "pick": array of keys to keep from input
//   - "omit": array of keys to remove from input
//   - "rename": map of old_key->new_key
//   - "input": step ID to use as input data (default: previous step)
//   - "merge": array of step IDs whose results are merged into one object
//
// This is much cheaper than agent delegation for simple data routing.
type TransformExecutor struct{}

func NewTransformExecutor() *TransformExecutor {
	return &TransformExecutor{}
}

func (e *TransformExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	result := make(map[string]any)

	applyInput(result, step.Config, wf)
	applyMerge(result, step.Config, wf)
	applySet(result, step.Config, wf)
	result = applyPick(result, step.Config)
	applyOmit(result, step.Config)
	applyRename(result, step.Config)

	step.Result = result
	wf.Context[step.ID] = result
	return nil
}

// applyInput loads the initial data from a referenced step result.
func applyInput(result map[string]any, cfg map[string]any, wf *Workflow) {
	inputRef, ok := cfg["input"].(string)
	if !ok {
		return
	}
	v, ok := wf.Context[inputRef]
	if !ok {
		return
	}
	if m, ok := v.(map[string]any); ok {
		for k, v := range m {
			result[k] = v
		}
	} else {
		result["_input"] = v
	}
}

// applyMerge combines multiple step results into the result map.
func applyMerge(result map[string]any, cfg map[string]any, wf *Workflow) {
	mergeRefs, ok := cfg["merge"].([]any)
	if !ok {
		return
	}
	for _, ref := range mergeRefs {
		refStr, ok := ref.(string)
		if !ok {
			continue
		}
		mergeContextValue(result, refStr, wf)
	}
}

// mergeContextValue merges a single context value into the result map.
func mergeContextValue(result map[string]any, key string, wf *Workflow) {
	v, ok := wf.Context[key]
	if !ok {
		return
	}
	if m, ok := v.(map[string]any); ok {
		for k, v := range m {
			result[k] = v
		}
	} else {
		result[key] = v
	}
}

// applySet adds or overwrites key-value pairs, resolving string references.
func applySet(result map[string]any, cfg map[string]any, wf *Workflow) {
	setMap, ok := cfg["set"].(map[string]any)
	if !ok {
		return
	}
	for k, v := range setMap {
		if s, ok := v.(string); ok {
			result[k] = resolvePromptRefs(s, wf)
		} else {
			result[k] = v
		}
	}
}

// applyPick keeps only specified keys from the result map.
func applyPick(result map[string]any, cfg map[string]any) map[string]any {
	pickArr, ok := cfg["pick"].([]any)
	if !ok {
		return result
	}
	picked := make(map[string]any)
	for _, k := range pickArr {
		if key, ok := k.(string); ok {
			if v, exists := result[key]; exists {
				picked[key] = v
			}
		}
	}
	return picked
}

// applyOmit removes specified keys from the result map.
func applyOmit(result map[string]any, cfg map[string]any) {
	omitArr, ok := cfg["omit"].([]any)
	if !ok {
		return
	}
	for _, k := range omitArr {
		if key, ok := k.(string); ok {
			delete(result, key)
		}
	}
}

// applyRename renames keys in the result map.
func applyRename(result map[string]any, cfg map[string]any) {
	renameMap, ok := cfg["rename"].(map[string]any)
	if !ok {
		return
	}
	for oldKey, newKeyRaw := range renameMap {
		if newKey, ok := newKeyRaw.(string); ok {
			if v, exists := result[oldKey]; exists {
				result[newKey] = v
				delete(result, oldKey)
			}
		}
	}
}
