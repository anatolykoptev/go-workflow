package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	triggerDirPerm  fs.FileMode = 0o750
	triggerFilePerm fs.FileMode = 0o600
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
	mu        sync.RWMutex
}

// NewTriggerService creates a trigger service backed by the given file path.
func NewTriggerService(storePath string, opts ...TriggerOption) *TriggerService {
	ts := &TriggerService{storePath: storePath}
	for _, opt := range opts {
		opt(ts)
	}
	ts.loadStore()
	return ts
}

// TriggerOption configures a TriggerService.
type TriggerOption func(*TriggerService)

// WithTriggerLogger sets the logger for the trigger service.
func WithTriggerLogger(l *slog.Logger) TriggerOption {
	return func(ts *TriggerService) { ts.logger = l }
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

	GlobalMetrics.TriggersEvaluated.Add(1)

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
		GlobalMetrics.TriggersFired.Add(int64(len(matched)))
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

// AddTrigger adds a new event trigger.
func (ts *TriggerService) AddTrigger(name, event string, filter map[string]string, action TriggerAction) (*EventTrigger, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if event == "" {
		return nil, errors.New("event is required")
	}
	if action.Kind == "" {
		return nil, errors.New("action kind is required")
	}

	id := fmt.Sprintf("trigger_%d", time.Now().UnixNano())
	trigger := EventTrigger{
		ID:        id,
		Name:      name,
		Enabled:   true,
		Event:     event,
		Filter:    filter,
		Action:    action,
		CreatedAt: time.Now().UnixMilli(),
	}

	ts.store.Triggers = append(ts.store.Triggers, trigger)
	if err := ts.saveStore(); err != nil {
		return nil, fmt.Errorf("save trigger: %w", err)
	}
	return &trigger, nil
}

// RemoveTrigger removes a trigger by ID.
func (ts *TriggerService) RemoveTrigger(id string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for i, t := range ts.store.Triggers {
		if t.ID == id {
			ts.store.Triggers = append(ts.store.Triggers[:i], ts.store.Triggers[i+1:]...)
			_ = ts.saveStore()
			return true
		}
	}
	return false
}

// EnableTrigger enables or disables a trigger.
func (ts *TriggerService) EnableTrigger(id string, enabled bool) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for i := range ts.store.Triggers {
		if ts.store.Triggers[i].ID == id {
			ts.store.Triggers[i].Enabled = enabled
			_ = ts.saveStore()
			return true
		}
	}
	return false
}

// ListTriggers returns a copy of all triggers.
func (ts *TriggerService) ListTriggers() []EventTrigger {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	out := make([]EventTrigger, len(ts.store.Triggers))
	copy(out, ts.store.Triggers)
	return out
}

func (ts *TriggerService) loadStore() {
	ts.store = &triggerStore{Version: 1, Triggers: []EventTrigger{}}

	data, err := os.ReadFile(ts.storePath)
	if err != nil {
		return
	}

	var store triggerStore
	if err := json.Unmarshal(data, &store); err != nil {
		ts.log().Warn("failed to parse trigger store",
			slog.String("path", ts.storePath),
			slog.String("error", err.Error()),
		)
		return
	}
	ts.store = &store
}

func (ts *TriggerService) saveStore() error {
	if err := os.MkdirAll(filepath.Dir(ts.storePath), triggerDirPerm); err != nil {
		return err
	}

	data, err := json.MarshalIndent(ts.store, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := ts.storePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, triggerFilePerm); err != nil {
		return err
	}
	return os.Rename(tmpPath, ts.storePath)
}
