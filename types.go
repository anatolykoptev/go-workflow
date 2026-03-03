package workflow

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// WorkflowState represents the lifecycle state of a workflow.
type WorkflowState string

const (
	StatePending         WorkflowState = "pending"
	StateRunning         WorkflowState = "running"
	StateWaitingApproval WorkflowState = "waiting_approval"
	StatePaused          WorkflowState = "paused"
	StateCompleted       WorkflowState = "completed"
	StateFailed          WorkflowState = "failed"
	StateCancelled       WorkflowState = "cancelled"
)

// StepState represents the lifecycle state of a single step.
type StepState string

const (
	StepPending   StepState = "pending"
	StepRunning   StepState = "running"
	StepCompleted StepState = "completed"
	StepFailed    StepState = "failed"
	StepSkipped      StepState = "skipped"
	StepDeadLettered StepState = "dead_lettered"
)

// StepKind defines the type of work a step performs.
type StepKind string

const (
	StepTool      StepKind = "tool"
	StepLLM       StepKind = "llm"
	StepApproval  StepKind = "approval"
	StepCondition StepKind = "condition"
	StepMessage   StepKind = "message"
	StepWorkflow  StepKind = "workflow"
	StepAgent     StepKind = "agent"
	StepTransform StepKind = "transform"
	StepA2A       StepKind = "a2a"
	StepForEach   StepKind = "foreach"
	StepBranchAll StepKind = "branchall"
	StepSuspend   StepKind = "suspend"
	StepNoop      StepKind = "noop"
)

// OnError strategy constants for step error handling.
const (
	OnErrorFail = "fail"
	OnErrorSkip = "skip"
)

// Built-in tool name constants used as step kind aliases.
const (
	ToolHTTPRequest = "http_request"
)

// stepKindAliases maps n8n-compatible and convenience names to canonical Vaelor step kinds.
var stepKindAliases = map[StepKind]StepKind{
	// n8n condition nodes
	"if":     StepCondition,
	"switch": StepCondition,
	"filter": StepCondition,

	// n8n data transform nodes
	"set":   StepTransform,
	"merge": StepTransform,

	// n8n code/function nodes
	"code":     StepAgent,
	"function": StepAgent,

	// n8n sub-workflow
	"execute_workflow": StepWorkflow,
	"sub_workflow":     StepWorkflow,

	// n8n messaging
	"send_message": StepMessage,
	"telegram":     StepMessage,
	"slack":        StepMessage,
	"notify":       StepMessage,

	// n8n HTTP request (shorthand — maps to tool with implicit tool=http_request)
	ToolHTTPRequest: StepTool,
	"http":          StepTool,
	"webhook":       StepTool,

	// n8n wait/delay
	"wait":  StepAgent,
	"delay": StepAgent,

	// Convenience aliases
	"ask":      StepApproval,
	"confirm":  StepApproval,
	"prompt":   StepLLM,
	"delegate": StepA2A,
}

// NormalizeStepKind resolves a step kind alias to the canonical Vaelor step kind.
// Returns the input unchanged if it's already canonical or unrecognized.
func NormalizeStepKind(kind StepKind) StepKind {
	if canonical, ok := stepKindAliases[kind]; ok {
		return canonical
	}
	return kind
}

// IsValidStepKind returns true if the kind is a known canonical or alias step kind.
func IsValidStepKind(kind StepKind) bool {
	switch kind {
	case StepTool, StepLLM, StepApproval, StepCondition, StepMessage, StepWorkflow, StepAgent, StepTransform, StepA2A, StepForEach, StepBranchAll, StepSuspend, StepNoop:
		return true
	}
	_, isAlias := stepKindAliases[kind]
	return isAlias
}

// Workflow is a multi-step execution plan with DAG dependencies and persistence.
type Workflow struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	TemplateName   string          `json:"template_name,omitempty"` // source template name (for concurrency guards)
	Description    string          `json:"description,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"` // dedup key — only one active workflow per key
	Steps         []Step          `json:"steps"`
	State         WorkflowState   `json:"state"`
	CurrentStep   string          `json:"current_step,omitempty"`
	Context       map[string]any  `json:"context"`
	Owner         string          `json:"owner"`
	AllowedTools  []string        `json:"allowed_tools,omitempty"` // restrict tool steps to these tools; empty = all allowed
	Security      *SecurityPolicy `json:"security,omitempty"`      // execution limits and constraints
	Error         string          `json:"error,omitempty"`
	StepsExecuted   int                    `json:"steps_executed,omitempty"`   // total steps executed (including retries)
	Reducers        map[string]ReducerKind `json:"reducers,omitempty"`        // per-key context merge strategy
	InterruptBefore []string               `json:"interrupt_before,omitempty"` // pause before these step IDs
	InterruptAfter  []string               `json:"interrupt_after,omitempty"`  // pause after these step IDs
	CreatedAt     int64                  `json:"created_at_ms"`
	UpdatedAt     int64           `json:"updated_at_ms"`
}

// Step is a single unit of work within a workflow.
type Step struct {
	ID        string         `json:"id"`
	Kind      StepKind       `json:"kind"`
	Config    map[string]any `json:"config"`
	DependsOn []string       `json:"depends_on,omitempty"`
	State     StepState      `json:"state"`
	Result    any            `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	Retries   int            `json:"retries,omitempty"`
	StartedAt int64          `json:"started_at_ms,omitempty"`
	EndedAt   int64          `json:"ended_at_ms,omitempty"`
}

// GetRetryMax returns the max retries from step config, default 0.
func (s *Step) GetRetryMax() int {
	r, ok := s.Config["retry"].(map[string]any)
	if !ok {
		return 0
	}
	max, _ := r["max"].(float64) // JSON numbers are float64
	return int(max)
}

// GetRetryDelayMS returns the retry delay from step config, default 1000ms.
func (s *Step) GetRetryDelayMS() int64 {
	r, ok := s.Config["retry"].(map[string]any)
	if !ok {
		return 1000
	}
	delay, _ := r["delay_ms"].(float64)
	if delay <= 0 {
		return 1000
	}
	return int64(delay)
}

// GetOnError returns the error handling strategy: "fail" (default) or "skip".
func (s *Step) GetOnError() string {
	if v, ok := s.Config["on_error"].(string); ok {
		return v
	}
	return OnErrorFail
}

// GetBackoffMultiplier returns the backoff multiplier from retry config, default 1.0 (no backoff).
func (s *Step) GetBackoffMultiplier() float64 {
	r, ok := s.Config["retry"].(map[string]any)
	if !ok {
		return 1.0
	}
	v, _ := r["backoff_multiplier"].(float64)
	if v <= 0 {
		return 1.0
	}
	return v
}

// GetMaxDelayMS returns the max delay cap from retry config, default 0 (no cap).
func (s *Step) GetMaxDelayMS() int64 {
	r, ok := s.Config["retry"].(map[string]any)
	if !ok {
		return 0
	}
	v, _ := r["max_delay_ms"].(float64)
	return int64(v)
}

// GetTimeoutMS returns the per-step timeout from config, default 0 (no timeout).
func (s *Step) GetTimeoutMS() int64 {
	v, _ := s.Config["timeout_ms"].(float64)
	return int64(v)
}

// GetRetryOn returns patterns that must match for retry to happen. Empty = retry on any error.
func (s *Step) GetRetryOn() []string {
	r, ok := s.Config["retry"].(map[string]any)
	if !ok {
		return nil
	}
	return toStringSlice(r["retry_on"])
}

// GetSkipOn returns patterns that skip retry if matched. Empty = never skip.
func (s *Step) GetSkipOn() []string {
	r, ok := s.Config["retry"].(map[string]any)
	if !ok {
		return nil
	}
	return toStringSlice(r["skip_on"])
}

// toStringSlice converts an any (expected []any of strings) to []string.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// calculateBackoff computes the retry delay with exponential backoff.
// attempt is 1-based (retry 1 uses multiplier^0 = baseMS).
func calculateBackoff(baseMS int64, attempt int, multiplier float64, maxMS int64) int64 {
	if multiplier <= 1.0 {
		return baseMS
	}
	delay := float64(baseMS) * math.Pow(multiplier, float64(attempt-1))
	d := int64(delay)
	if maxMS > 0 && d > maxMS {
		d = maxMS
	}
	return d
}

// removeString returns a new slice with all occurrences of val removed.
func removeString(ss []string, val string) []string {
	out := ss[:0:0] // nil if ss is nil
	for _, s := range ss {
		if s != val {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// matchesAnyPattern returns true if msg contains any pattern (case-insensitive substring).
func matchesAnyPattern(msg string, patterns []string) bool {
	lower := strings.ToLower(msg)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// NewWorkflow creates a workflow with sensible defaults.
func NewWorkflow(id, name, owner string, steps []Step) *Workflow {
	now := time.Now().UnixMilli()
	return &Workflow{
		ID:        id,
		Name:      name,
		Steps:     steps,
		State:     StatePending,
		Context:   make(map[string]any),
		Owner:     owner,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Clone returns a deep copy of the workflow.
func (w *Workflow) Clone() *Workflow {
	cp := *w
	cp.Steps = make([]Step, len(w.Steps))
	for i, s := range w.Steps {
		cp.Steps[i] = s
		cp.Steps[i].Config = deepCloneMap(s.Config)
		cp.Steps[i].DependsOn = slices.Clone(s.DependsOn)
	}
	cp.Context = deepCloneMap(w.Context)
	cp.AllowedTools = slices.Clone(w.AllowedTools)
	return &cp
}

// deepCloneMap creates a deep copy of a map[string]any via JSON round-trip.
func deepCloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return maps.Clone(m) // fallback to shallow
	}
	var cp map[string]any
	if json.Unmarshal(data, &cp) != nil {
		return maps.Clone(m)
	}
	return cp
}

// GetStep returns a pointer to the step with the given ID, or nil.
func (w *Workflow) GetStep(stepID string) *Step {
	for i := range w.Steps {
		if w.Steps[i].ID == stepID {
			return &w.Steps[i]
		}
	}
	return nil
}

// IsTerminal returns true if the workflow is in a final state.
func (w *Workflow) IsTerminal() bool {
	return w.State == StateCompleted || w.State == StateFailed || w.State == StateCancelled
}
