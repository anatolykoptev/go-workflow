package workflow

import "context"

// Hook event constants.
const (
	EventWorkflowStarted        = "workflow_started"
	EventWorkflowCompleted      = "workflow_completed"
	EventWorkflowFailed         = "workflow_failed"
	EventWorkflowCancelled      = "workflow_cancelled"
	EventWorkflowStepStarted    = "workflow_step_started"
	EventWorkflowStepCompleted  = "workflow_step_completed"
	EventWorkflowStepFailed     = "workflow_step_failed"
	EventWorkflowApprovalNeeded = "workflow_approval_needed"
)

// StepExecutor runs a single step within a workflow.
type StepExecutor interface {
	Execute(ctx context.Context, step *Step, wf *Workflow) error
}

// MessagePublisher delivers messages to users (replaces *bus.MessageBus).
type MessagePublisher interface {
	PublishOutbound(msg OutboundMessage)
}

// OutboundMessage is a message to be delivered to a user channel.
type OutboundMessage struct {
	Channel string
	ChatID  string
	Content string
}

// LLMProvider sends prompts to a language model (replaces providers.LLMProvider).
type LLMProvider interface {
	Chat(ctx context.Context, messages []LLMMessage, model string) (*LLMResponse, error)
	GetDefaultModel() string
}

// LLMMessage is a single message in a conversation.
//
// Images is an optional list of inline image attachments for multimodal
// providers. Text-only providers ignore this field; multimodal providers
// (Anthropic Claude, OpenAI GPT-4V, etc.) consume the bytes alongside the
// Content text. The field is additive — existing callers that leave it nil
// continue to behave as before.
type LLMMessage struct {
	Role    string
	Content string
	Images  []LLMImageContent
}

// LLMImageContent is an inline image payload attached to an LLMMessage.
// MIMEType examples: "image/png", "image/jpeg", "image/webp".
type LLMImageContent struct {
	Bytes    []byte
	MIMEType string
}

// VisionCapable is implemented by LLMProvider implementations that support
// multimodal input (text + images). The StepVision executor probes for this
// interface; providers that do not implement it (or return false) cause the
// vision step to log a warning and fall back to a text-only call with the
// prompt alone.
type VisionCapable interface {
	SupportsVision() bool
}

// LLMResponse is the model's reply with optional token usage data.
type LLMResponse struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
}
