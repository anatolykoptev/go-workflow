package workflow

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// EventTrigger fires a TriggerAction when a matching hook event occurs.
type EventTrigger struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	Event     string            `json:"event"`
	Filter    map[string]string `json:"filter"`
	Action    TriggerAction     `json:"action"`
	CreatedAt int64             `json:"created_at_ms"`
}

// TriggerAction defines what happens when a trigger fires.
type TriggerAction struct {
	Kind       string `json:"kind"`
	WorkflowID string `json:"workflow_id,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
	Message    string `json:"message,omitempty"`
	Channel    string `json:"channel,omitempty"`
	To         string `json:"to,omitempty"`
}

// TriggerExecutor is called when a trigger fires.
type TriggerExecutor func(trigger *EventTrigger) error

type triggerStore struct {
	Version  int            `json:"version"`
	Triggers []EventTrigger `json:"triggers"`
}

// TriggerService manages event-driven triggers with JSON persistence.
type TriggerService struct {
	storePath string
	store     *triggerStore
	executor  TriggerExecutor
	logger    *slog.Logger
	metrics   *Metrics
	mu        sync.RWMutex
}

// TriggerOption configures a TriggerService.
type TriggerOption func(*TriggerService)

// NewTriggerService creates a trigger service backed by the given file path.
func NewTriggerService(storePath string, opts ...TriggerOption) *TriggerService {
	ts := &TriggerService{storePath: storePath, metrics: GlobalMetrics}
	for _, opt := range opts {
		opt(ts)
	}
	ts.loadStore()
	return ts
}

// WithTriggerLogger sets the logger for the trigger service.
func WithTriggerLogger(l *slog.Logger) TriggerOption {
	return func(ts *TriggerService) { ts.logger = l }
}

// WithTriggerMetrics sets the metrics instance for the trigger service.
func WithTriggerMetrics(m *Metrics) TriggerOption {
	return func(ts *TriggerService) { ts.metrics = m }
}

func (ts *TriggerService) log() *slog.Logger {
	if ts.logger != nil {
		return ts.logger
	}
	return slog.Default()
}

// SetExecutor sets the callback for executing trigger actions.
func (ts *TriggerService) SetExecutor(fn TriggerExecutor) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.executor = fn
}

// HookHandler returns a function suitable for hooks.Registry.On.
func (ts *TriggerService) HookHandler(event string) func(data map[string]any) error {
	return func(data map[string]any) error {
		actions := ts.Evaluate(event, data)
		for i := range actions {
			trigger := &actions[i]
			ts.mu.RLock()
			exec := ts.executor
			ts.mu.RUnlock()
			if exec != nil {
				if err := exec(trigger); err != nil {
					ts.log().Warn("trigger execution failed",
						slog.String("trigger_id", trigger.ID),
						slog.String("event", event),
						slog.String("error", err.Error()),
					)
				}
			}
		}
		return nil
	}
}

// RegisterHooks registers hook handlers for all unique events in the store.
func (ts *TriggerService) RegisterHooks(hookRegistrar func(event string, fn func(map[string]any) error)) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	seen := make(map[string]bool)
	for _, t := range ts.store.Triggers {
		if !t.Enabled || seen[t.Event] {
			continue
		}
		seen[t.Event] = true
		hookRegistrar(t.Event, ts.HookHandler(t.Event))
	}

	if len(seen) > 0 {
		events := make([]string, 0, len(seen))
		for e := range seen {
			events = append(events, e)
		}
		ts.log().Info("registered event triggers",
			slog.Int("count", len(seen)),
			slog.String("events", strings.Join(events, ",")),
		)
	}
}

// Evaluate returns all matching triggers for the given event and data.
func (ts *TriggerService) Evaluate(event string, data map[string]any) []EventTrigger {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	ts.metrics.TriggersEvaluated.Add(1)

	var matched []EventTrigger
	for _, t := range ts.store.Triggers {
		if !t.Enabled || t.Event != event {
			continue
		}
		if MatchesFilter(t.Filter, data) {
			matched = append(matched, t)
		}
	}

	if len(matched) > 0 {
		ts.metrics.TriggersFired.Add(int64(len(matched)))
	}
	return matched
}

// MatchesFilter returns true if all filter key-value pairs match the data.
// Empty filter always matches. Values are compared case-insensitively.
func MatchesFilter(filter map[string]string, data map[string]any) bool {
	for k, v := range filter {
		dataVal, ok := data[k]
		if !ok {
			return false
		}
		if !strings.EqualFold(fmt.Sprintf("%v", dataVal), v) {
			return false
		}
	}
	return true
}
