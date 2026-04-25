package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestInMemoryCache_RoundTrip(t *testing.T) {
	t.Parallel()
	c := NewInMemoryCache()
	entry := StepCacheEntry{
		Output:   map[string]any{"k": "v"},
		StoredAt: time.Now(),
	}
	if err := c.Put(context.Background(), "k1", entry); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := c.Get(context.Background(), "k1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	out := got.Output.(map[string]any)
	if out["k"] != "v" {
		t.Errorf("got %+v", got.Output)
	}
}

func TestInMemoryCache_Miss(t *testing.T) {
	t.Parallel()
	c := NewInMemoryCache()
	_, ok, err := c.Get(context.Background(), "missing")
	if err != nil || ok {
		t.Errorf("expected miss; got ok=%v err=%v", ok, err)
	}
}

func TestFileCache_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "cache")
	c, err := NewFileCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry := StepCacheEntry{
		Output:   map[string]any{"foo": "bar"},
		StoredAt: time.Now(),
	}
	if err := c.Put(context.Background(), "abc", entry); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := c.Get(context.Background(), "abc")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	out, _ := got.Output.(map[string]any)
	if out["foo"] != "bar" {
		t.Errorf("got %+v", got.Output)
	}
}

func TestStepCacheEntry_Expired(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name    string
		entry   StepCacheEntry
		expired bool
	}{
		{"no ttl", StepCacheEntry{StoredAt: now.Add(-time.Hour), TTL: 0}, false},
		{"fresh", StepCacheEntry{StoredAt: now.Add(-time.Second), TTL: time.Minute}, false},
		{"stale", StepCacheEntry{StoredAt: now.Add(-2 * time.Minute), TTL: time.Minute}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.expired(now); got != tc.expired {
				t.Errorf("expired = %v, want %v", got, tc.expired)
			}
		})
	}
}

func TestStepCacheKey_DiffersByDependsOn(t *testing.T) {
	t.Parallel()
	// Same kind + config, different DependsOn must hash to different keys —
	// otherwise two structurally identical transforms in different DAG
	// positions collide.
	store := newTestStore(t)
	engine := NewEngine(store, WithMetrics(NewMetrics()), WithStepCache(NewInMemoryCache()))
	wf := &Workflow{ID: "wf"}
	stepA := &Step{ID: "a", Kind: StepTransform, Config: map[string]any{"set": map[string]any{"x": 1}}, DependsOn: []string{"prev1"}}
	stepB := &Step{ID: "b", Kind: StepTransform, Config: map[string]any{"set": map[string]any{"x": 1}}, DependsOn: []string{"prev2"}}
	keyA, okA := engine.stepCacheKey(wf, stepA)
	keyB, okB := engine.stepCacheKey(wf, stepB)
	if !okA || !okB {
		t.Fatalf("expected both cacheable; ok=%v,%v", okA, okB)
	}
	if keyA == keyB {
		t.Errorf("keys must differ; both = %s", keyA)
	}
}

func TestStepCacheKey_StableAcrossMapOrder(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithMetrics(NewMetrics()), WithStepCache(NewInMemoryCache()))
	wf := &Workflow{ID: "wf"}
	a := &Step{ID: "a", Kind: StepTransform, Config: map[string]any{"x": 1, "y": 2}}
	b := &Step{ID: "b", Kind: StepTransform, Config: map[string]any{"y": 2, "x": 1}}
	ka, _ := engine.stepCacheKey(wf, a)
	kb, _ := engine.stepCacheKey(wf, b)
	if ka != kb {
		t.Errorf("map-order should not affect key; %s vs %s", ka, kb)
	}
}

func TestStepCacheKey_NotCacheableByDefault(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithMetrics(NewMetrics()), WithStepCache(NewInMemoryCache()))
	// StepTool is NOT in the default allowlist — must not cache.
	step := &Step{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "x"}}
	if _, ok := engine.stepCacheKey(&Workflow{}, step); ok {
		t.Error("StepTool should not be cacheable by default")
	}
	// StepTransform IS the default — must cache.
	step2 := &Step{ID: "b", Kind: StepTransform, Config: map[string]any{}}
	if _, ok := engine.stepCacheKey(&Workflow{}, step2); !ok {
		t.Error("StepTransform should be cacheable by default")
	}
}

func TestStepCacheKey_WithExplicitKinds(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithMetrics(NewMetrics()), WithStepCache(NewInMemoryCache()), WithStepCacheKinds(StepTool))
	step := &Step{ID: "a", Kind: StepTool, Config: map[string]any{"tool": "x"}}
	if _, ok := engine.stepCacheKey(&Workflow{}, step); !ok {
		t.Error("StepTool should be cacheable when explicitly enabled")
	}
	stepT := &Step{ID: "b", Kind: StepTransform, Config: map[string]any{}}
	if _, ok := engine.stepCacheKey(&Workflow{}, stepT); ok {
		t.Error("StepTransform should NOT be cacheable when only StepTool is enabled")
	}
}

func TestStepCache_HitSkipsExecution(t *testing.T) {
	t.Parallel()

	// Run the same transform workflow twice with caching enabled. The second
	// run should hit the cache and not call the transform executor again.
	store := newTestStore(t)
	engine := NewEngine(store, WithMetrics(NewMetrics()), WithStepCache(NewInMemoryCache()))

	build := func(id string) *Workflow {
		return NewWorkflow(id, "TransformWF", "owner", []Step{
			{ID: "t", Kind: StepTransform, Config: map[string]any{
				"set": map[string]any{"out": "value1"},
			}, State: StepPending},
		})
	}

	wf1 := build("wf-cache-1")
	if err := store.Save(wf1); err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background(), "wf-cache-1"); err != nil {
		t.Fatalf("first run: %v", err)
	}

	hitsBefore := engine.getMetrics().StepCacheHits.Load()
	missesBefore := engine.getMetrics().StepCacheMisses.Load()

	wf2 := build("wf-cache-2")
	if err := store.Save(wf2); err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background(), "wf-cache-2"); err != nil {
		t.Fatalf("second run: %v", err)
	}

	hitsAfter := engine.getMetrics().StepCacheHits.Load()
	missesAfter := engine.getMetrics().StepCacheMisses.Load()
	if hitsAfter-hitsBefore != 1 {
		t.Errorf("cache hits delta = %d, want 1", hitsAfter-hitsBefore)
	}
	if missesAfter != missesBefore {
		t.Errorf("misses changed from %d to %d on second run", missesBefore, missesAfter)
	}

	loaded, _ := store.Load("wf-cache-2")
	if loaded.State != StateCompleted {
		t.Errorf("second wf state = %s, want completed", loaded.State)
	}
	step := loaded.GetStep("t")
	if step.State != StepCompleted {
		t.Errorf("step state = %s, want completed", step.State)
	}
	res, _ := step.Result.(map[string]any)
	if res["out"] != "value1" {
		t.Errorf("step result = %+v, want out=value1", step.Result)
	}
}

func TestStepCache_HitPreservesCost(t *testing.T) {
	t.Parallel()

	cache := NewInMemoryCache()
	store := newTestStore(t)
	engine := NewEngine(store, WithStepCache(cache))

	// Manually seed a cache entry that includes a Cost. After a cache hit,
	// the workflow's aggregate Cost must reflect the cached cost so budgets
	// and accounting still see the dollars.
	cost := &StepCost{StepID: "t", Kind: StepTransform, USDEstimate: 0.42}
	step := &Step{ID: "t", Kind: StepTransform, Config: map[string]any{"set": map[string]any{"out": "v"}}, DependsOn: nil}
	wf := &Workflow{}
	key, ok := engine.stepCacheKey(wf, step)
	if !ok {
		t.Fatal("expected cacheable")
	}
	_ = cache.Put(context.Background(), key, StepCacheEntry{
		Output:   map[string]any{"out": "v"},
		Cost:     cost,
		StoredAt: time.Now(),
	})

	wfReal := NewWorkflow("wf-cost", "WF", "owner", []Step{
		{ID: "t", Kind: StepTransform, Config: map[string]any{"set": map[string]any{"out": "v"}}, State: StepPending},
	})
	if err := store.Save(wfReal); err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background(), "wf-cost"); err != nil {
		t.Fatalf("start: %v", err)
	}

	loaded, _ := store.Load("wf-cost")
	if loaded.Cost == nil {
		t.Fatal("cost not preserved on cache hit")
	}
	if loaded.Cost.USDEstimate < 0.41 || loaded.Cost.USDEstimate > 0.43 {
		t.Errorf("cost USD = %v, want ~0.42", loaded.Cost.USDEstimate)
	}
}

func TestStepCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	cache := NewInMemoryCache()
	store := newTestStore(t)
	engine := NewEngine(store, WithStepCache(cache))

	step := &Step{ID: "t", Kind: StepTransform, Config: map[string]any{"set": map[string]any{"out": "stale"}}}
	key, _ := engine.stepCacheKey(&Workflow{}, step)
	_ = cache.Put(context.Background(), key, StepCacheEntry{
		Output:   map[string]any{"out": "stale"},
		StoredAt: time.Now().Add(-time.Hour),
		TTL:      time.Minute,
	})

	wf := NewWorkflow("wf-ttl", "WF", "owner", []Step{
		{ID: "t", Kind: StepTransform, Config: map[string]any{"set": map[string]any{"out": "fresh"}}, State: StepPending},
	})
	_ = store.Save(wf)
	if err := engine.Start(context.Background(), "wf-ttl"); err != nil {
		t.Fatalf("start: %v", err)
	}

	loaded, _ := store.Load("wf-ttl")
	res, _ := loaded.GetStep("t").Result.(map[string]any)
	// Expired entry should have been ignored — fresh execution produces "fresh".
	if res["out"] != "fresh" {
		t.Errorf("expired cache served stale; got %+v", res)
	}
}

func TestNewFileCache_EmptyDirRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewFileCache(""); err == nil {
		t.Error("expected error for empty dir")
	}
}
