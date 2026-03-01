package workflow

import "fmt"

// --- Dry-run validation (addresses n8n testing/production parity gap) ---

// ValidationError describes a single issue found during workflow validation.
type ValidationError struct {
	StepID  string `json:"step_id,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (ve ValidationError) Error() string {
	if ve.StepID != "" {
		return fmt.Sprintf("step %s: %s", ve.StepID, ve.Message)
	}
	return ve.Message
}

// ValidateWorkflow performs a dry-run validation of a workflow without executing it.
// Catches: missing deps, invalid step kinds, unknown tools, DAG cycles, empty configs.
// Returns nil if the workflow is valid.
func (e *Engine) ValidateWorkflow(wf *Workflow) []ValidationError {
	var errs []ValidationError

	if wf.Name == "" {
		errs = append(errs, ValidationError{Message: "workflow name is empty"})
	}
	if len(wf.Steps) == 0 {
		errs = append(errs, ValidationError{Message: "workflow has no steps"})
		return errs
	}

	// Build step ID set
	stepIDs := make(map[string]bool, len(wf.Steps))
	for _, s := range wf.Steps {
		if s.ID == "" {
			errs = append(errs, ValidationError{StepID: "(empty)", Message: "step has empty ID"})
			continue
		}
		if stepIDs[s.ID] {
			errs = append(errs, ValidationError{StepID: s.ID, Message: "duplicate step ID"})
		}
		stepIDs[s.ID] = true
	}

	for _, s := range wf.Steps {
		// Check step kind is valid
		normalized := NormalizeStepKind(s.Kind)
		if _, ok := e.executors[normalized]; !ok {
			errs = append(errs, ValidationError{
				StepID:  s.ID,
				Field:   "kind",
				Message: fmt.Sprintf("unknown step kind %q", s.Kind),
			})
		}

		// Check depends_on references exist
		for _, dep := range s.DependsOn {
			if !stepIDs[dep] {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "depends_on",
					Message: fmt.Sprintf("depends on non-existent step %q", dep),
				})
			}
			if dep == s.ID {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "depends_on",
					Message: "step depends on itself",
				})
			}
		}

		// Check tool steps reference a tool
		if normalized == StepTool {
			toolName, _ := s.Config["tool"].(string)
			if toolName == "" {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "config.tool",
					Message: "tool step missing 'tool' in config",
				})
			}
		}

		// Check agent steps have a task
		if normalized == StepAgent {
			task, _ := s.Config["task"].(string)
			if task == "" {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "config.task",
					Message: "agent step missing 'task' in config",
				})
			}
		}

		// Check a2a steps have agent_id and message
		if normalized == StepA2A {
			agentID, _ := s.Config["agent_id"].(string)
			if agentID == "" {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "config.agent_id",
					Message: "a2a step missing 'agent_id' in config",
				})
			}
			msg, _ := s.Config["message"].(string)
			if msg == "" {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "config.message",
					Message: "a2a step missing 'message' in config",
				})
			}
		}

		// Check on_error references a valid step if it's a branch
		onError := s.GetOnError()
		if onError != OnErrorFail && onError != OnErrorSkip && onError != "" {
			if !stepIDs[onError] {
				errs = append(errs, ValidationError{
					StepID:  s.ID,
					Field:   "on_error",
					Message: fmt.Sprintf("on_error references non-existent step %q", onError),
				})
			}
		}
	}

	// Check for DAG cycles
	if cycle := detectCycle(wf.Steps); cycle != "" {
		errs = append(errs, ValidationError{
			Message: "dependency cycle detected: " + cycle,
		})
	}

	return errs
}

// detectCycle returns a description of the first cycle found, or "" if acyclic.
func detectCycle(steps []Step) string {
	// Build adjacency: step → deps
	deps := make(map[string][]string)
	for _, s := range steps {
		deps[s.ID] = s.DependsOn
	}

	const (
		white = 0 // unvisited
		gray  = 1 // in progress
		black = 2 // done
	)
	color := make(map[string]int)

	var dfs func(id string) string
	dfs = func(id string) string {
		color[id] = gray
		for _, dep := range deps[id] {
			if color[dep] == gray {
				return fmt.Sprintf("%s → %s", id, dep)
			}
			if color[dep] == white {
				if cycle := dfs(dep); cycle != "" {
					return cycle
				}
			}
		}
		color[id] = black
		return ""
	}

	for _, s := range steps {
		if color[s.ID] == white {
			if cycle := dfs(s.ID); cycle != "" {
				return cycle
			}
		}
	}
	return ""
}
