package workflow

import "fmt"

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

	stepIDs := collectStepIDs(wf.Steps, &errs)

	for _, s := range wf.Steps {
		e.validateStepKind(s, &errs)
		validateStepDeps(s, stepIDs, &errs)
		validateStepConfig(s, &errs)
		validateStepRetry(s, &errs)
		validateStepTimeout(s, &errs)
		validateStepOnError(s, stepIDs, &errs)
	}

	if cycle := detectCycle(wf.Steps); cycle != "" {
		errs = append(errs, ValidationError{
			Message: "dependency cycle detected: " + cycle,
		})
	}

	return errs
}

// collectStepIDs builds a set of step IDs, reporting duplicates and empty IDs.
func collectStepIDs(steps []Step, errs *[]ValidationError) map[string]bool {
	ids := make(map[string]bool, len(steps))
	for _, s := range steps {
		if s.ID == "" {
			*errs = append(*errs, ValidationError{StepID: "(empty)", Message: "step has empty ID"})
			continue
		}
		if ids[s.ID] {
			*errs = append(*errs, ValidationError{StepID: s.ID, Message: "duplicate step ID"})
		}
		ids[s.ID] = true
	}
	return ids
}

// validateStepKind checks that the step kind is recognized by the engine.
func (e *Engine) validateStepKind(s Step, errs *[]ValidationError) {
	normalized := NormalizeStepKind(s.Kind)
	if _, ok := e.executors[normalized]; !ok {
		*errs = append(*errs, ValidationError{
			StepID:  s.ID,
			Field:   "kind",
			Message: fmt.Sprintf("unknown step kind %q", s.Kind),
		})
	}
}

// validateStepDeps checks that depends_on references exist and don't self-reference.
func validateStepDeps(s Step, stepIDs map[string]bool, errs *[]ValidationError) {
	for _, dep := range s.DependsOn {
		if !stepIDs[dep] {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "depends_on",
				Message: fmt.Sprintf("depends on non-existent step %q", dep),
			})
		}
		if dep == s.ID {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "depends_on",
				Message: "step depends on itself",
			})
		}
	}
}

// validateStepConfig checks kind-specific required config fields.
func validateStepConfig(s Step, errs *[]ValidationError) {
	normalized := NormalizeStepKind(s.Kind)

	switch normalized {
	case StepTool:
		if toolName, _ := s.Config["tool"].(string); toolName == "" {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "config.tool",
				Message: "tool step missing 'tool' in config",
			})
		}
	case StepAgent:
		if task, _ := s.Config["task"].(string); task == "" {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "config.task",
				Message: "agent step missing 'task' in config",
			})
		}
	case StepA2A:
		if agentID, _ := s.Config["agent_id"].(string); agentID == "" {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "config.agent_id",
				Message: "a2a step missing 'agent_id' in config",
			})
		}
		if msg, _ := s.Config["message"].(string); msg == "" {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "config.message",
				Message: "a2a step missing 'message' in config",
			})
		}
	}
}

// validateStepRetry checks retry config values for consistency.
func validateStepRetry(s Step, errs *[]ValidationError) {
	if s.GetRetryMax() <= 0 {
		return
	}
	if s.GetRetryDelayMS() <= 0 {
		*errs = append(*errs, ValidationError{
			StepID:  s.ID,
			Field:   "config.retry.delay_ms",
			Message: "retry delay_ms must be > 0",
		})
	}
	if s.GetBackoffMultiplier() < 1.0 {
		*errs = append(*errs, ValidationError{
			StepID:  s.ID,
			Field:   "config.retry.backoff_multiplier",
			Message: "backoff_multiplier must be >= 1.0",
		})
	}
	if s.GetMaxDelayMS() < 0 {
		*errs = append(*errs, ValidationError{
			StepID:  s.ID,
			Field:   "config.retry.max_delay_ms",
			Message: "max_delay_ms must be >= 0",
		})
	}
}

// validateStepTimeout checks per-step timeout value.
func validateStepTimeout(s Step, errs *[]ValidationError) {
	if s.GetTimeoutMS() < 0 {
		*errs = append(*errs, ValidationError{
			StepID:  s.ID,
			Field:   "config.timeout_ms",
			Message: "timeout_ms must be >= 0",
		})
	}
}

// validateStepOnError checks that on_error branch references exist.
func validateStepOnError(s Step, stepIDs map[string]bool, errs *[]ValidationError) {
	onError := s.GetOnError()
	if onError != OnErrorFail && onError != OnErrorSkip && onError != OnErrorContinue && onError != "" {
		if !stepIDs[onError] {
			*errs = append(*errs, ValidationError{
				StepID:  s.ID,
				Field:   "on_error",
				Message: fmt.Sprintf("on_error references non-existent step %q", onError),
			})
		}
	}
}

// ValidateTemplate reports an error if the template references a step kind
// for which no executor is registered on this engine. The error message
// names the first missing kind and hints at the With*Provider option that
// would register it. Use this at template-load or workflow-create time so
// authoring + wiring gaps surface BEFORE the workflow ever runs.
//
// Returns nil for nil templates (caller's responsibility to nil-check).
//
// Note: ValidateWorkflow above validates an already-instantiated Workflow
// (DAG cycles, unknown tool refs, retry config). ValidateTemplate is the
// one-step-earlier check — does the engine even have executors for the
// kinds this template will need?
func (e *Engine) ValidateTemplate(t *Template) error {
	if t == nil {
		return nil
	}
	for _, ts := range t.Steps {
		kind := NormalizeStepKind(ts.Kind)
		if _, ok := e.executors[kind]; ok {
			continue
		}
		return fmt.Errorf(
			"template %q: step %q requires kind %q but no executor is registered (%s)",
			t.Name, ts.ID, kind, executorRegistrationHint(kind),
		)
	}
	return nil
}

// executorRegistrationHint returns a short hint about which EngineOption
// registers an executor for the given step kind. Used by ValidateTemplate to
// turn a "missing executor" failure into actionable wiring guidance.
func executorRegistrationHint(kind StepKind) string {
	switch kind {
	case StepLLM:
		return "register one with WithLLMProvider or WithLLMClient"
	case StepVision:
		return "register one with WithLLMProvider (vision-capable) or WithVisionProvider"
	case StepTool:
		return "register one with WithMCPServers or WithToolRunner"
	case StepImage:
		return "register one with WithImageRenderer"
	case StepAgent:
		return "register one with WithAgentRunner"
	case StepA2A:
		return "register one with WithA2ACaller"
	case StepMessage:
		return "register one with WithMessenger"
	}
	return "see go-workflow EngineOption docs for the relevant Provider"
}
