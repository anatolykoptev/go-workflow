package workflow

import (
	"encoding/json"
	"maps"
	"math"
	"os"
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

// BlockingStep returns the pending approval gate the workflow is currently
// halted on when State==StateWaitingApproval, else nil. It discriminates three
// cases for a workflow in the waiting-approval state:
//
//  1. Primary/authoritative: CurrentStep names a pending approval step — that
//     is the gate the workflow is actually blocked on; return it. CurrentStep
//     is written at step-start and preserved across the approval pause, so it
//     is the authoritative "which gate am I blocked on" signal in this case.
//
//  2. Interrupt-pause/no-op: CurrentStep names a NON-approval step that is an
//     active interrupt_before/interrupt_after pause point (the step an
//     interrupt checkpoint paused on — see handleInterrupt / the
//     interrupt_after block in completeStep). There is NO approval gate
//     pending resolution here at all; the pause is just a checkpoint. Return
//     nil so HandleApproval only clears the interrupt list and flips state,
//     touching nothing else. Falling through to the scan here would grab an
//     UNRELATED downstream approval gate that hasn't even been reached yet and
//     silently bypass it (see issue #23 round 4).
//
//  3. Race-fallback/scan: CurrentStep is empty, dangling, or raced to an
//     unrelated non-interrupt non-approval sibling under parallel dispatch
//     (runParallel/DispatchBatch siblings each write CurrentStep inside
//     store.Modify; last-write-wins can leave it pointing at a non-approval
//     sibling after the real gate already paused the workflow). Scan the
//     committed Steps state (race-immune — it reads the step slice, not the
//     racy CurrentStep field) for the first pending approval step. This also
//     covers workflows persisted before CurrentStep was reliable.
func (w *Workflow) BlockingStep() *Step {
	if w.State != StateWaitingApproval {
		return nil
	}
	if s := w.GetStep(w.CurrentStep); s != nil {
		if s.Kind == StepApproval && s.State == StepPending {
			return s
		}
		// CurrentStep legitimately names a non-approval interrupt pause
		// point (interrupt_before/interrupt_after) — nothing to resolve
		// here, do NOT fall through to the scan (that would incorrectly
		// grab an unrelated downstream approval gate — see #23 round 4).
		if slices.Contains(w.InterruptBefore, w.CurrentStep) || slices.Contains(w.InterruptAfter, w.CurrentStep) {
			return nil
		}
	}
	// Fallback: CurrentStep is empty, dangling, or raced to an unrelated
	// non-interrupt non-approval sibling under parallel dispatch (see #23
	// round 3) — scan committed Steps state for the real pending gate.
	for i := range w.Steps {
		if w.Steps[i].Kind == StepApproval && w.Steps[i].State == StepPending {
			return &w.Steps[i]
		}
	}
	return nil
}

// IsTerminal returns true if the workflow is in a final state.
func (w *Workflow) IsTerminal() bool {
	return w.State == StateCompleted || w.State == StateFailed || w.State == StateCancelled
}

// ResumableArtifact reports whether a cancelled workflow is resumable via
// Reopen (CurrentStep is a pending approval gate — the same condition
// Reopen checks before transitioning back to StateWaitingApproval), and
// when it is, surfaces the most recent surviving artifact path recorded
// in a completed approval step's data.
//
// This is the status-side counterpart to Reopen: Reopen ACTS on the
// CurrentStep/pending-approval condition, ResumableArtifact REPORTS it so
// wf_status can distinguish a genuinely terminal failure (StateFailed
// from a dead-lettered step — nothing to reopen into) from a recoverable
// "step failed / BLOCKed, artifacts intact" cancel — see issue #296.
//
// Returns (false, "") when:
//   - the workflow is not cancelled (running, waiting, paused, completed,
//     or failed — only StateCancelled is reopen-eligible),
//   - CurrentStep is empty or points at a non-approval / non-pending step
//     (Reopen would refuse; surfacing resumable=true would over-promise),
//   - or no completed approval step recorded a file path in its data.
//
// The artifact-path scan walks Steps in REVERSE order (most recent
// completed approval first) and inspects Context[stepID] — the exact
// store HandleApprovalWithData writes the approver's data payload to.
// It looks for a string value that looks like a filesystem artifact path
// (currently: a /tmp/ prefix, matching the go-wp wp_temp /tmp/go-wp/{project}/
// convention where the merged render lives). The key name in the data map
// is operator-controlled (templates say "Pass file path as data" without
// pinning a key), so the scan is key-agnostic — any string field holding a
// /tmp/ path qualifies. No filesystem existence check is performed: the
// path is what was recorded at approval time, and statting would couple
// this pure read to the filesystem; the caller can stat if it cares.
func (w *Workflow) ResumableArtifact() (resumable bool, artifactPath string) {
	if w.State != StateCancelled || w.CurrentStep == "" {
		return false, ""
	}
	step := w.GetStep(w.CurrentStep)
	if step == nil || step.Kind != StepApproval || step.State != StepPending {
		return false, ""
	}
	// Walk ALL completed steps most-recent-first for a surviving artifact
	// path. Not approval-only: the merged render is usually produced by a
	// tool/executor step that records its output path in Context[stepID], so
	// restricting the scan to approval steps would silently miss it (PR #42
	// review — the written-but-not-wired risk). Returns (true, "") when
	// resumable but no path was recorded — the operator can still
	// reopen+re-approve, they just don't get a salvageable-file hint.
	for i := len(w.Steps) - 1; i >= 0; i-- {
		s := w.Steps[i]
		if s.State != StepCompleted {
			continue
		}
		if path, ok := extractArtifactPath(w.Context[s.ID]); ok {
			return true, path
		}
	}
	return true, ""
}

// extractArtifactPath inspects a step's recorded Context payload for a string
// that looks like a filesystem artifact path. The data shape is
// operator-controlled (templates say "pass the file path as data" without
// pinning a key), so the scan is key-agnostic and recurses one level into a
// nested map (e.g. {"result":{"render_file":…}}). When several candidates
// exist within one payload the smallest (sorted) is returned for a
// DETERMINISTIC result — Go map iteration order is otherwise random (PR #42
// review). Returns ("", false) when no artifact-like path is found.
func extractArtifactPath(v any) (string, bool) {
	var candidates []string
	var scan func(any, int)
	scan = func(x any, depth int) {
		switch val := x.(type) {
		case string:
			if isArtifactPath(val) {
				candidates = append(candidates, val)
			}
		case map[string]any:
			if depth > 1 {
				return
			}
			for _, field := range val {
				scan(field, depth+1)
			}
		}
	}
	scan(v, 0)
	if len(candidates) == 0 {
		return "", false
	}
	slices.Sort(candidates)
	return candidates[0], true
}

// isArtifactPath reports whether s looks like a filesystem artifact path worth
// surfacing as a surviving artifact location. Matches an absolute path under
// the OS temp dir (os.TempDir(), e.g. /var/folders/… on macOS) OR the literal
// /tmp/ prefix (the go-wp wp_temp /tmp/go-wp/{project}/ convention holds even
// when $TMPDIR differs). Narrow on purpose: a false positive would point the
// operator at a non-artifact string and undermine the signal.
func isArtifactPath(s string) bool {
	if strings.HasPrefix(s, "/tmp/") {
		return true
	}
	tmp := os.TempDir()
	return tmp != "" && tmp != "/" && strings.HasPrefix(s, strings.TrimRight(tmp, "/")+"/")
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
