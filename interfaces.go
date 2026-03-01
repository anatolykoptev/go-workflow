package workflow

import "context"

// Hook event constants (extracted from Vaelor pkg/hooks).
const (
	EventWorkflowStarted       = "workflow_started"
	EventWorkflowCompleted     = "workflow_completed"
	EventWorkflowFailed        = "workflow_failed"
	EventWorkflowCancelled     = "workflow_cancelled"
	EventWorkflowStepStarted   = "workflow_step_started"
	EventWorkflowStepCompleted = "workflow_step_completed"
	EventWorkflowStepFailed    = "workflow_step_failed"
	EventWorkflowApprovalNeeded = "workflow_approval_needed"
)

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
type LLMMessage struct {
	Role    string
	Content string
}

// LLMResponse is the model's reply with optional token usage data.
type LLMResponse struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
}
