package workflow

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
	StepPending      StepState = "pending"
	StepRunning      StepState = "running"
	StepCompleted    StepState = "completed"
	StepFailed       StepState = "failed"
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
	StepImage     StepKind = "image"
)

// OnError strategy constants for step error handling.
const (
	OnErrorFail = "fail"
	OnErrorSkip = "skip"
	// OnErrorContinue is an alias for OnErrorSkip: step is marked skipped and
	// execution continues with downstream steps. Useful in n8n-style definitions
	// where "continue" is a natural value for continueOnFail.
	OnErrorContinue = "continue"
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

	// Image rendering aliases — common naming variants for the image primitive.
	"render_image": StepImage,
	"image_render": StepImage,
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
	case StepTool, StepLLM, StepApproval, StepCondition, StepMessage, StepWorkflow, StepAgent, StepTransform, StepA2A, StepForEach, StepBranchAll, StepSuspend, StepNoop, StepImage:
		return true
	}
	_, isAlias := stepKindAliases[kind]
	return isAlias
}

// Workflow is a multi-step execution plan with DAG dependencies and persistence.
type Workflow struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	TemplateName    string                 `json:"template_name,omitempty"` // source template name (for concurrency guards)
	Description     string                 `json:"description,omitempty"`
	IdempotencyKey  string                 `json:"idempotency_key,omitempty"` // dedup key — only one active workflow per key
	Steps           []Step                 `json:"steps"`
	State           WorkflowState          `json:"state"`
	CurrentStep     string                 `json:"current_step,omitempty"`
	Context         map[string]any         `json:"context"`
	Owner           string                 `json:"owner"`
	AllowedTools    []string               `json:"allowed_tools,omitempty"` // restrict tool steps to these tools; empty = all allowed
	Security        *SecurityPolicy        `json:"security,omitempty"`      // execution limits and constraints
	Error           string                 `json:"error,omitempty"`
	StepsExecuted   int                    `json:"steps_executed,omitempty"`   // total steps executed (including retries)
	Reducers        map[string]ReducerKind `json:"reducers,omitempty"`         // per-key context merge strategy
	InterruptBefore []string               `json:"interrupt_before,omitempty"` // pause before these step IDs
	InterruptAfter  []string               `json:"interrupt_after,omitempty"`  // pause after these step IDs
	CreatedAt       int64                  `json:"created_at_ms"`
	UpdatedAt       int64                  `json:"updated_at_ms"`
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
	// AlwaysRun marks the step for teardown-on-failure semantics. When true,
	// the step runs even after the workflow has entered StateFailed, provided
	// every direct dependency has reached a terminal state (completed, skipped,
	// failed, or dead-lettered). Dependencies still in StepPending block it
	// (so cleanup never runs with unmet inputs).
	AlwaysRun bool `json:"always_run,omitempty"`
}
