package workflow

import (
	"context"
	"fmt"
)

// MessageExecutor publishes a message to the bus for delivery to the user.
type MessageExecutor struct {
	bus MessagePublisher
}

func NewMessageExecutor(bus MessagePublisher) *MessageExecutor {
	return &MessageExecutor{bus: bus}
}

func (e *MessageExecutor) Execute(_ context.Context, step *Step, wf *Workflow) error {
	content, _ := step.Config["content"].(string)
	if content == "" {
		// Use result from a referenced step
		if ref, ok := step.Config["content_from"].(string); ok {
			if val, ok := wf.Context[ref]; ok {
				content = fmt.Sprintf("%v", val)
			}
		}
	}

	if content == "" {
		return fmt.Errorf("step %s: missing 'content' or 'content_from' in config", step.ID)
	}

	content = resolvePromptRefs(content, wf)

	// Parse owner (format: "channel:chatID")
	channel, chatID := ParseOwner(wf.Owner)
	if channel == "" || chatID == "" {
		return fmt.Errorf("step %s: invalid owner format %q (expected channel:chatID)", step.ID, wf.Owner)
	}

	e.bus.PublishOutbound(OutboundMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: content,
	})

	step.Result = "delivered"
	wf.Context[step.ID] = "delivered"
	return nil
}
