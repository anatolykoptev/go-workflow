package workflow

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

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

// AddCost merges a single step's cost into the workflow aggregate, creating the
// aggregate map on first call. Safe to call from inside step executors after
// they record their own StepCost.
func (w *Workflow) AddCost(c StepCost) {
	if w.Cost == nil {
		w.Cost = &WorkflowCost{BySteps: make(map[string]StepCost)}
	}
	if w.Cost.BySteps == nil {
		w.Cost.BySteps = make(map[string]StepCost)
	}
	w.Cost.InputTokens += c.InputTokens
	w.Cost.OutputTokens += c.OutputTokens
	w.Cost.USDEstimate += c.USDEstimate
	if c.Kind == StepImage {
		w.Cost.ImagesRendered++
	}
	w.Cost.BytesRendered += c.Bytes
	w.Cost.BySteps[c.StepID] = c
	w.Cost.UpdatedAt = time.Now()
}
