package workflow

import (
	"context"
	"fmt"
	"log/slog"
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

	// Also check "text" key — alias used in n8n-style templates
	if content == "" {
		if text, ok := step.Config["text"].(string); ok {
			content = text
		}
	}

	if content == "" {
		return fmt.Errorf("step %s: missing 'content' or 'content_from' in config", step.ID)
	}

	content = resolvePromptRefs(content, wf)

	// Fallback: if bus is nil, log to stdout and complete successfully
	if e.bus == nil {
		slog.Info("workflow message", slog.String("step", step.ID), slog.String("text", content))
		step.Result = content
		wf.Context[step.ID] = content
		return nil
	}

	// Parse owner (format: "channel:chatID")
	channel, chatID := ParseOwner(wf.Owner)
	if channel == "" || chatID == "" {
		slog.Info("workflow message (no owner)", slog.String("step", step.ID), slog.String("text", content))
		step.Result = content
		wf.Context[step.ID] = content
		return nil
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
