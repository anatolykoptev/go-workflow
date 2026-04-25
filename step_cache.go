package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// StepCache is a content-addressed store for step outputs. Implementations
// are pluggable; built-in InMemoryCache (process-local) and FileCache
// (cross-process via filesystem) cover the common cases.
//
// Cache keys are SHA-256 of canonicalized JSON of (kind, config,
// depends_on) — see stepCacheKey. The cache is a side-channel: hits
// short-circuit executor.Execute and replay both Output and Cost, so
// Workflow.Cost still reflects the cached cost as if the step had run.
type StepCache interface {
	Get(ctx context.Context, key string) (StepCacheEntry, bool, error)
	Put(ctx context.Context, key string, entry StepCacheEntry) error
}

// StepCacheEntry is one cached step result. Cost is preserved verbatim so
// downstream budget enforcement and reporting see the same dollars as a
// fresh execution would.
type StepCacheEntry struct {
	Output   any
	Context  map[string]any
	Cost     *StepCost
	StoredAt time.Time
	TTL      time.Duration // 0 = no expiration
}

// expired returns true once StoredAt + TTL is in the past. TTL=0 disables
// expiry; entries persist until the backing store is cleared.
func (e StepCacheEntry) expired(now time.Time) bool {
	if e.TTL == 0 {
		return false
	}
	return now.Sub(e.StoredAt) > e.TTL
}

// WithStepCache plugs in a cache backend. When unset, the engine never
// looks up or writes cache entries.
func WithStepCache(cache StepCache) EngineOption {
	return func(e *Engine) {
		e.stepCache = cache
	}
}

// WithStepCacheKinds restricts caching to a specific set of step kinds.
// Defaults to [StepTransform] when WithStepCache is set without this option
// — transforms are pure data shaping and always safe to cache.
//
// Caller is responsible for confirming determinism of any kind they enable
// (e.g. StepLLM with temperature=0).
func WithStepCacheKinds(kinds ...StepKind) EngineOption {
	return func(e *Engine) {
		if len(kinds) == 0 {
			return
		}
		m := make(map[StepKind]bool, len(kinds))
		for _, k := range kinds {
			m[k] = true
		}
		e.stepCacheKinds = m
	}
}

// File-mode constants for FileCache. 0o750 (rwxr-x---) and 0o600 (rw-------)
// keep cache contents readable only by the owner / owner-group, satisfying
// gosec G301/G306.
const (
	cacheDirMode  = 0o750
	cacheFileMode = 0o600
)

// stepCacheKey computes the content hash for a step. Returns ("", false)
// when caching is disabled or the step kind is not configured for caching.
func (e *Engine) stepCacheKey(_ *Workflow, step *Step) (string, bool) {
	if e.stepCache == nil {
		return "", false
	}
	if !e.isCacheable(step.Kind) {
		return "", false
	}
	payload := map[string]any{
		"kind":       string(step.Kind),
		"config":     canonicalizeForHash(step.Config),
		"depends_on": step.DependsOn,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true
}

// isCacheable reports whether the given step kind is on the cache allowlist.
// Defaults to {StepTransform} when no explicit allowlist was configured.
func (e *Engine) isCacheable(kind StepKind) bool {
	if e.stepCacheKinds == nil {
		return kind == StepTransform
	}
	return e.stepCacheKinds[kind]
}

// canonicalizeForHash produces a stable representation of any JSON-y value
// so that map ordering does not affect the hash. Sorts maps, recurses into
// slices, returns scalars unchanged.
func canonicalizeForHash(v any) any {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, [2]any{k, canonicalizeForHash(val[k])})
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = canonicalizeForHash(item)
		}
		return out
	default:
		return val
	}
}

// stepCacheGet looks up an entry, dropping expired ones transparently. Errors
// from the backend are logged at debug — a backend hiccup must NEVER break
// the workflow, so we return (zero, false, nil).
func (e *Engine) stepCacheGet(ctx context.Context, key string) (StepCacheEntry, bool, error) {
	if e.stepCache == nil || key == "" {
		return StepCacheEntry{}, false, nil
	}
	entry, ok, err := e.stepCache.Get(ctx, key)
	if err != nil {
		e.log().Debug("step_cache get failed", "key", key, "error", err.Error())
		return StepCacheEntry{}, false, nil
	}
	if !ok {
		return StepCacheEntry{}, false, nil
	}
	if entry.expired(time.Now()) {
		return StepCacheEntry{}, false, nil
	}
	return entry, true, nil
}

// stepCachePut stores the most recent step output + cost under the given
// key. No-op when caching is disabled.
func (e *Engine) stepCachePut(ctx context.Context, key string, wf *Workflow, step *Step) error {
	if e.stepCache == nil || key == "" {
		return nil
	}
	entry := StepCacheEntry{
		Output:   step.Result,
		StoredAt: time.Now(),
	}
	// Capture the step's contribution to wf.Cost so a future hit can
	// re-add the same dollars.
	if wf != nil && wf.Cost != nil && wf.Cost.BySteps != nil {
		if c, ok := wf.Cost.BySteps[step.ID]; ok {
			cc := c
			entry.Cost = &cc
		}
	}
	// Mirror any context entry the executor produced under the step ID.
	if wf != nil {
		if v, ok := wf.Context[step.ID]; ok {
			entry.Context = map[string]any{step.ID: v}
		}
	}
	if err := e.stepCache.Put(ctx, key, entry); err != nil {
		e.log().Debug("step_cache put failed", "key", key, "error", err.Error())
		return err
	}
	return nil
}

// applyCacheHit replays a cached entry into the workflow + step state. It
// mirrors the on-success path of completeStep so downstream consumers
// observe identical state regardless of whether the step ran or hit the
// cache. Records a tiny tracing span tagged step.cache_hit=true.
func (e *Engine) applyCacheHit(ctx context.Context, workflowID, stepID string, w *Workflow, step *Step, _ string, entry StepCacheEntry) error {
	endedAt := time.Now().UnixMilli()

	// Tracing — short span specifically for the hit so backends can count
	// hits/misses without scanning every step span.
	_, span := e.startStepSpan(ctx, w, step)
	finishStepSpan(span, step, 0, true, nil)

	// Mark step running then completed atomically so observers see the same
	// transitions as a fresh run.
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepRunning
			s.StartedAt = endedAt
		}
		w.CurrentStep = stepID
		w.UpdatedAt = endedAt
	})

	// Re-apply the cached cost to the persisted workflow so budgets and
	// reports reflect the same dollars they would on a fresh run.
	// recordStepCost mutates the in-memory `w`; mirror it under store.Modify
	// so the persisted copy carries the cost too.
	if entry.Cost != nil {
		_ = e.store.Modify(workflowID, func(persisted *Workflow) {
			persisted.AddCost(*entry.Cost)
		})
		// Also bump the per-engine running totals (microcents + token counts).
		if m := e.getMetrics(); m != nil {
			m.WorkflowTokensInputTotal.Add(entry.Cost.InputTokens)
			m.WorkflowTokensOutputTotal.Add(entry.Cost.OutputTokens)
			if entry.Cost.USDEstimate > 0 {
				m.WorkflowCostUSDTotal.Add(uint64(entry.Cost.USDEstimate*usdToMicrocents + usdRoundHalf))
			}
		}
	}

	stepCtx := map[string]any{}
	for k, v := range entry.Context {
		stepCtx[k] = v
	}
	stepCtx[stepID] = entry.Output

	e.getMetrics().StepsExecuted.Add(1)

	if m := e.getMetrics(); m != nil {
		m.StepCacheHits.Add(1)
	}

	e.log().Info("step cache hit",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
		"kind", string(step.Kind),
	)

	return e.completeStep(workflowID, stepID, w, step, entry.Output, stepCtx, endedAt)
}

// --- InMemoryCache --------------------------------------------------------

// InMemoryCache is a process-local map-backed StepCache. Suitable for unit
// tests and single-process deployments where re-running a workflow inside
// the same process is the cache-hit pathway.
type InMemoryCache struct {
	mu      sync.RWMutex
	entries map[string]StepCacheEntry
}

// NewInMemoryCache returns a fresh in-memory cache.
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{entries: make(map[string]StepCacheEntry)}
}

func (c *InMemoryCache) Get(_ context.Context, key string) (StepCacheEntry, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		return StepCacheEntry{}, false, nil
	}
	return entry, true, nil
}

func (c *InMemoryCache) Put(_ context.Context, key string, entry StepCacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
	return nil
}

// --- FileCache ------------------------------------------------------------

// FileCache stores entries as JSON files under a directory keyed by hash.
// Persists across processes; safe to share between sibling workers but does
// not provide locking — use InMemoryCache + dispatcher coordination if you
// need single-flight semantics.
type FileCache struct {
	dir string
}

// NewFileCache returns a file-backed cache rooted at dir. Creates the dir
// lazily on first Put. Returns an error only if dir is empty.
func NewFileCache(dir string) (*FileCache, error) {
	if dir == "" {
		return nil, errors.New("step_cache: file cache dir must be non-empty")
	}
	return &FileCache{dir: dir}, nil
}

func (c *FileCache) path(key string) string {
	return filepath.Join(c.dir, key+".json")
}

func (c *FileCache) Get(_ context.Context, key string) (StepCacheEntry, bool, error) {
	raw, err := os.ReadFile(c.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return StepCacheEntry{}, false, nil
		}
		return StepCacheEntry{}, false, err
	}
	var encoded fileCacheRecord
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return StepCacheEntry{}, false, err
	}
	return StepCacheEntry{
		Output:   encoded.Output,
		Context:  encoded.Context,
		Cost:     encoded.Cost,
		StoredAt: encoded.StoredAt,
		TTL:      time.Duration(encoded.TTLNanos),
	}, true, nil
}

func (c *FileCache) Put(_ context.Context, key string, entry StepCacheEntry) error {
	if err := os.MkdirAll(c.dir, cacheDirMode); err != nil {
		return err
	}
	rec := fileCacheRecord{
		Output:   entry.Output,
		Context:  entry.Context,
		Cost:     entry.Cost,
		StoredAt: entry.StoredAt,
		TTLNanos: int64(entry.TTL),
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := c.path(key) + ".tmp"
	if err := os.WriteFile(tmp, raw, cacheFileMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path(key)); err != nil {
		return fmt.Errorf("step_cache: rename %s: %w", c.path(key), err)
	}
	return nil
}

type fileCacheRecord struct {
	Output   any            `json:"output,omitempty"`
	Context  map[string]any `json:"context,omitempty"`
	Cost     *StepCost      `json:"cost,omitempty"`
	StoredAt time.Time      `json:"stored_at"`
	TTLNanos int64          `json:"ttl_nanos,omitempty"`
}
