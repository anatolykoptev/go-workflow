package workflow

// ReducerKind defines how a context key is updated when a step writes to it.
type ReducerKind string

const (
	ReducerReplace ReducerKind = "replace"
	ReducerAppend  ReducerKind = "append"
	ReducerSum     ReducerKind = "sum"
	ReducerMerge   ReducerKind = "merge"
)

// applyReducer updates ctx[key] according to the reducer kind.
func applyReducer(ctx map[string]any, key string, value any, kind ReducerKind) {
	switch kind {
	case ReducerAppend:
		existing, _ := ctx[key].([]any)
		ctx[key] = append(existing, value)

	case ReducerSum:
		ctx[key] = numericAdd(ctx[key], value)

	case ReducerMerge:
		incoming, ok := value.(map[string]any)
		if !ok {
			ctx[key] = value
			return
		}
		existing, _ := ctx[key].(map[string]any)
		if existing == nil {
			existing = make(map[string]any, len(incoming))
		}
		for k, v := range incoming {
			existing[k] = v
		}
		ctx[key] = existing

	default: // replace (or empty string)
		ctx[key] = value
	}
}

// numericAdd adds two numeric values. Supports float64 and int64.
// If a is nil, returns b. Falls back to replacement on type mismatch.
func numericAdd(a, b any) any {
	if a == nil {
		return b
	}
	switch av := a.(type) {
	case float64:
		if bv, ok := b.(float64); ok {
			return av + bv
		}
	case int64:
		if bv, ok := b.(int64); ok {
			return av + bv
		}
	}
	return b // type mismatch — replace
}

// mergeContext applies step output to the workflow context using configured reducers.
func mergeContext(wf *Workflow, stepContext map[string]any) {
	for k, v := range stepContext {
		kind := ReducerReplace
		if wf.Reducers != nil {
			if rk, ok := wf.Reducers[k]; ok {
				kind = rk
			}
		}
		applyReducer(wf.Context, k, v, kind)
	}
}
