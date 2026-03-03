package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const (
	triggerDirPerm  fs.FileMode = 0o750
	triggerFilePerm fs.FileMode = 0o600
)

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
