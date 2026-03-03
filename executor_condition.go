package workflow

import (
	"context"
	"fmt"
	"strings"
)

// ConditionExecutor evaluates a simple expression and skips or proceeds.
// Config: {"check": "stepID", "contains": "keyword"}
//
// Operators: "contains", "equals", "not_empty" (bool).
//
//	"not_empty" treats "", "<nil>", "[]", "[ ]", "{}", and "null" as empty.
//
// If "stop_workflow" is true and the condition evaluates to false,
// the step returns an error which stops the workflow immediately
// instead of silently passing "false" to downstream steps.
type ConditionExecutor struct{}

func NewConditionExecutor() *ConditionExecutor {
	return &ConditionExecutor{}
}

func (e *ConditionExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	check, _ := step.Config["check"].(string)
	if check == "" {
		return fmt.Errorf("step %s: missing 'check' in config", step.ID)
	}

	val := fmt.Sprintf("%v", resolveRef(check, wf))

	passed := false

	if contains, ok := step.Config["contains"].(string); ok {
		passed = strings.Contains(strings.ToLower(val), strings.ToLower(contains))
	} else if equals, ok := step.Config["equals"].(string); ok {
		passed = strings.EqualFold(val, equals)
	} else if notEmpty, ok := step.Config["not_empty"].(bool); ok && notEmpty {
		passed = !isEmptyValue(val)
	} else {
		return fmt.Errorf("step %s: condition needs 'contains', 'equals', or 'not_empty'", step.ID)
	}

	if passed {
		step.Result = "true" //nolint:goconst
		wf.Context[step.ID] = "true"
	} else {
		step.Result = "false" //nolint:goconst
		wf.Context[step.ID] = "false"

		// stop_workflow: fail immediately instead of passing "false" downstream
		if stop, ok := step.Config["stop_workflow"].(bool); ok && stop {
			msg, _ := step.Config["message"].(string)
			if msg == "" {
				msg = fmt.Sprintf("condition %q not met", step.ID)
			}
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}

// isEmptyValue returns true for strings that represent empty/nil data.
func isEmptyValue(s string) bool {
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "", "<nil>", "[]", "{}", "null", "[ ]", "{ }":
		return true
	}
	return false
}
