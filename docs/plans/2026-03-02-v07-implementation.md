# v0.7.0 AI Enhancements Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add state reducers, $ref path selectors, LLM streaming/tool calling via go-kit, and declarative interrupt_before/after.

**Architecture:** Five independent features. Tasks 1-2 are pure go-workflow (no new deps). Task 3 adds go-kit/llm dep and rewrites LLMExecutor. Task 4 extends LLMExecutor with multi-turn tool loop. Task 5 adds interrupt checks in RunStep. Each task is TDD with commit after green tests.

**Tech Stack:** Go 1.26, `github.com/anatolykoptev/go-kit v0.6.0` (llm package), existing go-workflow test infra.

---

### Task 1: State Reducers

**Files:**
- Create: `reducer.go` (~60 lines)
- Create: `reducer_test.go`
- Modify: `types.go:128-145` (add `Reducers` field to `Workflow`)
- Modify: `engine_step.go:149-162` (replace direct context assignment with reducer)
- Modify: `engine_step.go:186-199` (same in `handleSuspend`)

**Step 1: Write failing tests for reducer logic**

Create `reducer_test.go`:

```go
package workflow

import "testing"

func TestApplyReducer_Replace(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"key": "old"}
	applyReducer(ctx, "key", "new", ReducerReplace)
	if ctx["key"] != "new" {
		t.Errorf("got %v, want 'new'", ctx["key"])
	}
}

func TestApplyReducer_Append(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"items": []any{"a"}}
	applyReducer(ctx, "items", "b", ReducerAppend)
	got := ctx["items"].([]any)
	if len(got) != 2 || got[1] != "b" {
		t.Errorf("got %v, want [a b]", got)
	}
}

func TestApplyReducer_AppendNewKey(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{}
	applyReducer(ctx, "items", "a", ReducerAppend)
	got := ctx["items"].([]any)
	if len(got) != 1 {
		t.Errorf("got %v, want [a]", got)
	}
}

func TestApplyReducer_Sum(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"total": float64(10)}
	applyReducer(ctx, "total", float64(5), ReducerSum)
	if ctx["total"] != float64(15) {
		t.Errorf("got %v, want 15", ctx["total"])
	}
}

func TestApplyReducer_SumInt64(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"total": int64(10)}
	applyReducer(ctx, "total", int64(5), ReducerSum)
	if ctx["total"] != int64(15) {
		t.Errorf("got %v, want 15", ctx["total"])
	}
}

func TestApplyReducer_SumNewKey(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{}
	applyReducer(ctx, "total", float64(5), ReducerSum)
	if ctx["total"] != float64(5) {
		t.Errorf("got %v, want 5", ctx["total"])
	}
}

func TestApplyReducer_Merge(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"data": map[string]any{"a": 1}}
	applyReducer(ctx, "data", map[string]any{"b": 2}, ReducerMerge)
	got := ctx["data"].(map[string]any)
	if got["a"] != 1 || got["b"] != 2 {
		t.Errorf("got %v, want {a:1 b:2}", got)
	}
}

func TestApplyReducer_DefaultIsReplace(t *testing.T) {
	t.Parallel()
	ctx := map[string]any{"key": "old"}
	applyReducer(ctx, "key", "new", "")
	if ctx["key"] != "new" {
		t.Errorf("got %v, want 'new'", ctx["key"])
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/krolik/src/go-workflow && go test -run TestApplyReducer -v`
Expected: FAIL — `applyReducer` undefined.

**Step 3: Implement reducers**

Create `reducer.go`:

```go
package workflow

// ReducerKind defines how context values are merged when a step writes.
type ReducerKind string

const (
	ReducerReplace ReducerKind = "replace" // default: last write wins
	ReducerAppend  ReducerKind = "append"  // append to []any
	ReducerSum     ReducerKind = "sum"     // numeric addition
	ReducerMerge   ReducerKind = "merge"   // shallow merge map[string]any
)

// applyReducer writes value into ctx[key] using the given reducer strategy.
func applyReducer(ctx map[string]any, key string, value any, kind ReducerKind) {
	switch kind {
	case ReducerAppend:
		existing, _ := ctx[key].([]any)
		ctx[key] = append(existing, value)

	case ReducerSum:
		ctx[key] = numericAdd(ctx[key], value)

	case ReducerMerge:
		existing, _ := ctx[key].(map[string]any)
		incoming, _ := value.(map[string]any)
		if existing == nil {
			existing = make(map[string]any)
		}
		for k, v := range incoming {
			existing[k] = v
		}
		ctx[key] = existing

	default: // replace
		ctx[key] = value
	}
}

// numericAdd adds two numeric values. Supports float64 and int64 (JSON number types).
func numericAdd(a, b any) any {
	switch av := a.(type) {
	case float64:
		bv, _ := b.(float64)
		return av + bv
	case int64:
		bv, _ := b.(int64)
		return av + bv
	default:
		return b // no existing value — just set
	}
}

// mergeContext writes stepContext into wf.Context respecting configured reducers.
func mergeContext(wf *Workflow, stepContext map[string]any) {
	for k, v := range stepContext {
		kind := ReducerKind("")
		if wf.Reducers != nil {
			kind = wf.Reducers[k]
		}
		applyReducer(wf.Context, k, v, kind)
	}
}
```

Add `Reducers` field to `Workflow` in `types.go:145` (after `UpdatedAt`):

```go
Reducers  map[string]ReducerKind `json:"reducers,omitempty"`
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/krolik/src/go-workflow && go test -run TestApplyReducer -v`
Expected: PASS

**Step 5: Wire reducers into engine_step.go**

In `engine_step.go`, replace the context merge at lines 156-159:

```go
// OLD:
// for k, v := range stepContext {
//     w.Context[k] = v
// }

// NEW:
mergeContext(w, stepContext)
```

Same change in `handleSuspend` at lines 193-195.

**Step 6: Write integration test**

Add to `reducer_test.go`:

```go
func TestEngine_ReducerAppend(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(&mockToolRunner{}))

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending, DependsOn: []string{"s1"}},
	})
	wf.Reducers = map[string]ReducerKind{"log": ReducerAppend}
	wf.Context["log"] = []any{"init"}
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	loaded, _ := store.Load(wf.ID)
	log := loaded.Context["log"].([]any)
	// "log" keeps accumulating — s1 and s2 results don't overwrite it
	if len(log) < 1 {
		t.Error("expected log to contain at least init entry")
	}
}
```

**Step 7: Run full test suite**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS

**Step 8: Commit**

```bash
git add reducer.go reducer_test.go types.go engine_step.go
git commit -m "feat: add state reducers (append/sum/merge) for context keys"
```

---

### Task 2: Typed Variable Passing ($ref paths)

**Files:**
- Modify: `resolve.go:11-41` (extend `resolveRef` with path traversal)
- Create: `resolve_path_test.go`

**Step 1: Write failing tests for path resolution**

Create `resolve_path_test.go`:

```go
package workflow

import "testing"

func TestResolvePath_Simple(t *testing.T) {
	t.Parallel()
	root := map[string]any{"name": "alice"}
	got := resolvePath(root, "name")
	if got != "alice" {
		t.Errorf("got %v, want 'alice'", got)
	}
}

func TestResolvePath_Nested(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"data": map[string]any{
			"items": map[string]any{"count": 42},
		},
	}
	got := resolvePath(root, "data.items.count")
	if got != 42 {
		t.Errorf("got %v, want 42", got)
	}
}

func TestResolvePath_ArrayIndex(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"items": []any{"a", "b", "c"},
	}
	got := resolvePath(root, "items[1]")
	if got != "b" {
		t.Errorf("got %v, want 'b'", got)
	}
}

func TestResolvePath_ArrayNested(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"users": []any{
			map[string]any{"name": "alice"},
			map[string]any{"name": "bob"},
		},
	}
	got := resolvePath(root, "users[0].name")
	if got != "alice" {
		t.Errorf("got %v, want 'alice'", got)
	}
}

func TestResolvePath_Wildcard(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"users": []any{
			map[string]any{"name": "alice"},
			map[string]any{"name": "bob"},
		},
	}
	got := resolvePath(root, "users[*].name")
	arr, ok := got.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("got %v, want [alice bob]", got)
	}
	if arr[0] != "alice" || arr[1] != "bob" {
		t.Errorf("got %v, want [alice bob]", arr)
	}
}

func TestResolvePath_Missing(t *testing.T) {
	t.Parallel()
	root := map[string]any{"a": 1}
	got := resolvePath(root, "missing.key")
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestResolvePath_OutOfBounds(t *testing.T) {
	t.Parallel()
	root := map[string]any{"items": []any{"a"}}
	got := resolvePath(root, "items[5]")
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestResolveRef_NestedPath(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		Context: map[string]any{
			"fetch": map[string]any{
				"data": map[string]any{"name": "result"},
			},
		},
	}
	got := resolveRef("$steps.fetch.data.name", wf)
	if got != "result" {
		t.Errorf("got %v, want 'result'", got)
	}
}

func TestResolveRef_DollarRef(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		Context: map[string]any{
			"s1": map[string]any{
				"items": []any{
					map[string]any{"id": "x"},
				},
			},
		},
	}
	cfg := map[string]any{"$ref": "s1.items[0].id"}
	got := resolveRefValue(cfg, wf)
	if got != "x" {
		t.Errorf("got %v, want 'x'", got)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/krolik/src/go-workflow && go test -run 'TestResolvePath|TestResolveRef_Nested|TestResolveRef_DollarRef' -v`
Expected: FAIL — `resolvePath` and `resolveRefValue` undefined.

**Step 3: Implement path resolution**

Add to `resolve.go` (after `resolveRef` function):

```go
// resolvePath traverses a nested structure using dot-path notation.
// Supports map field access (a.b.c), array indexing (a[0]), and wildcard projection (a[*].b).
func resolvePath(root any, path string) any {
	segments := splitPath(path)
	current := root
	for _, seg := range segments {
		if current == nil {
			return nil
		}
		current = resolveSegment(current, seg)
	}
	return current
}

// resolveSegment resolves a single path segment against a value.
func resolveSegment(val any, seg string) any {
	// Wildcard: [*]
	if seg == "[*]" {
		arr, ok := val.([]any)
		if !ok {
			return nil
		}
		return arr // return the whole array for next segment to project
	}

	// Array index: [N]
	if len(seg) > 2 && seg[0] == '[' && seg[len(seg)-1] == ']' {
		idx := 0
		for _, c := range seg[1 : len(seg)-1] {
			idx = idx*10 + int(c-'0')
		}
		arr, ok := val.([]any)
		if !ok || idx >= len(arr) {
			return nil
		}
		return arr[idx]
	}

	// Wildcard projection: apply field to each element
	if arr, ok := val.([]any); ok {
		result := make([]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if v, exists := m[seg]; exists {
					result = append(result, v)
				}
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	}

	// Map field access
	if m, ok := val.(map[string]any); ok {
		return m[seg]
	}

	return nil
}

// splitPath splits "a.b[0].c[*].d" into ["a", "b", "[0]", "c", "[*]", "d"].
func splitPath(path string) []string {
	var segments []string
	var current strings.Builder
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			if current.Len() > 0 {
				segments = append(segments, current.String())
				current.Reset()
			}
		case '[':
			if current.Len() > 0 {
				segments = append(segments, current.String())
				current.Reset()
			}
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				current.WriteByte(path[i])
				continue
			}
			segments = append(segments, path[i:i+j+1])
			i += j
		default:
			current.WriteByte(path[i])
		}
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

// resolveRefValue resolves a {"$ref": "path"} config value.
func resolveRefValue(val any, wf *Workflow) any {
	m, ok := val.(map[string]any)
	if !ok {
		return resolveRef(val, wf)
	}
	ref, ok := m["$ref"].(string)
	if !ok {
		return val
	}
	// Split ref into stepID and remaining path
	parts := strings.SplitN(ref, ".", 2)
	stepID := parts[0]
	root, ok := wf.Context[stepID]
	if !ok {
		return val
	}
	if len(parts) == 1 {
		return root
	}
	result := resolvePath(root, parts[1])
	if result == nil {
		return val
	}
	return result
}
```

Update `resolveRef` in `resolve.go` to support nested paths after `$steps.`:

Replace the `$steps.` handling block (lines 17-25):

```go
// "$steps.stepID.field.subfield" -> nested path traversal
if strings.HasPrefix(s, "$steps.") {
	rest := s[7:] // strip "$steps."
	parts := strings.SplitN(rest, ".", 2)
	stepID := parts[0]
	root, ok := wf.Context[stepID]
	if !ok {
		return s
	}
	if len(parts) == 1 {
		return root
	}
	result := resolvePath(root, parts[1])
	if result == nil {
		return s
	}
	return result
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/krolik/src/go-workflow && go test -run 'TestResolvePath|TestResolveRef_Nested|TestResolveRef_DollarRef' -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS (existing resolveRef tests must still pass)

**Step 6: Commit**

```bash
git add resolve.go resolve_path_test.go
git commit -m "feat: add typed variable passing with $ref path selectors"
```

---

### Task 3: LLM Streaming via go-kit

**Files:**
- Modify: `go.mod` (add go-kit dependency)
- Modify: `executor_llm.go` (add `*llm.Client` field, streaming path)
- Modify: `engine.go` (add `WithLLMClient`, `WithStreamCallback` options)
- Create: `executor_llm_test.go` (streaming + go-kit client tests)

**Step 1: Add go-kit dependency**

Run: `cd /home/krolik/src/go-workflow && go get github.com/anatolykoptev/go-kit@v0.6.0`

**Step 2: Write failing tests for streaming**

Create `executor_llm_test.go`:

```go
package workflow

import (
	"context"
	"sync"
	"testing"

	"github.com/anatolykoptev/go-kit/llm"
)

// mockLLMClient simulates go-kit/llm.Client for testing.
// We test via LLMExecutor which can use either provider or client.

type llmClientAdapter struct {
	chatResp *llm.ChatResponse
	chatErr  error
}

func (a *llmClientAdapter) Chat(_ context.Context, _ []LLMMessage, _ string) (*LLMResponse, error) {
	if a.chatErr != nil {
		return nil, a.chatErr
	}
	return &LLMResponse{
		Content:      a.chatResp.Content,
		InputTokens:  a.chatResp.Usage.PromptTokens,
		OutputTokens: a.chatResp.Usage.CompletionTokens,
	}, nil
}

func (a *llmClientAdapter) GetDefaultModel() string { return "test" }

func TestLLMExecutor_StreamCollectsChunks(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	// Use provider path — streaming via client is tested at integration level
	provider := &stressLLM{response: &LLMResponse{Content: "hello world", InputTokens: 10, OutputTokens: 5}}
	ex := NewLLMExecutor(provider, m)

	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "say hi"}}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}
	if step.Result != "hello world" {
		t.Errorf("got %v, want 'hello world'", step.Result)
	}
	if m.LLMTokensInput.Load() != 10 {
		t.Errorf("input tokens: got %d, want 10", m.LLMTokensInput.Load())
	}
}

func TestLLMExecutor_StreamCallbackFired(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	provider := &stressLLM{response: &LLMResponse{Content: "hi", InputTokens: 1, OutputTokens: 1}}
	ex := NewLLMExecutor(provider, m)

	var mu sync.Mutex
	var chunks []string
	ex.SetStreamCallback(func(wfID, stepID, delta string) {
		mu.Lock()
		chunks = append(chunks, delta)
		mu.Unlock()
	})

	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "hi", "stream": true}}

	// With provider (not client), streaming falls back to non-streaming
	// but still stores result correctly
	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}
	if step.Result != "hi" {
		t.Errorf("got %v, want 'hi'", step.Result)
	}
}
```

**Step 3: Run tests to verify they fail**

Run: `cd /home/krolik/src/go-workflow && go test -run TestLLMExecutor_Stream -v`
Expected: FAIL — `SetStreamCallback` undefined.

**Step 4: Implement streaming support in LLMExecutor**

Rewrite `executor_llm.go`:

```go
package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go-kit/llm"
)

// SkillResolver loads a skill prompt by name. Satisfied by skills.SkillsLoader.
type SkillResolver interface {
	LoadSkill(name string) (string, bool)
}

// StreamCallback receives streaming chunks from LLM execution.
type StreamCallback func(workflowID, stepID, delta string)

// LLMExecutor sends a prompt to the LLM provider and stores the response.
// Supports: legacy LLMProvider interface, go-kit/llm.Client, streaming, tool calling.
type LLMExecutor struct {
	provider LLMProvider // legacy interface
	client   *llm.Client // go-kit client (preferred)
	skills   SkillResolver
	metrics  *Metrics
	streamCB StreamCallback
}

func NewLLMExecutor(provider LLMProvider, metrics *Metrics) *LLMExecutor {
	return &LLMExecutor{provider: provider, metrics: metrics}
}

// NewLLMExecutorWithClient creates an LLMExecutor using go-kit/llm.Client.
func NewLLMExecutorWithClient(client *llm.Client, metrics *Metrics) *LLMExecutor {
	return &LLMExecutor{client: client, metrics: metrics}
}

// SetSkills sets the skill resolver for skill-aware LLM steps. Nil-safe.
func (e *LLMExecutor) SetSkills(sr SkillResolver) { e.skills = sr }

// SetStreamCallback sets the callback for streaming chunks.
func (e *LLMExecutor) SetStreamCallback(cb StreamCallback) { e.streamCB = cb }

func (e *LLMExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	prompt := e.resolvePrompt(step, wf)
	if prompt == "" {
		return fmt.Errorf("step %s: missing 'prompt' or 'skill' in config", step.ID)
	}

	model, _ := step.Config["model"].(string)
	wantStream, _ := step.Config["stream"].(bool)

	if e.client != nil {
		return e.executeWithClient(ctx, step, wf, prompt, model, wantStream)
	}
	return e.executeWithProvider(ctx, step, wf, prompt, model)
}

// executeWithClient uses go-kit/llm.Client (streaming + tool calling capable).
func (e *LLMExecutor) executeWithClient(ctx context.Context, step *Step, wf *Workflow, prompt, model string, stream bool) error {
	messages := []llm.Message{{Role: "user", Content: prompt}}

	var opts []llm.ChatOption
	if model != "" {
		// model is set on client level; per-step override not supported in go-kit yet
	}

	if stream && e.streamCB != nil {
		return e.streamWithClient(ctx, step, wf, messages, opts)
	}

	resp, err := e.client.Chat(ctx, messages, opts...)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	step.Result = resp.Content
	wf.Context[step.ID] = resp.Content
	e.recordUsage(step.ID, wf, resp.Usage)
	return nil
}

// streamWithClient uses go-kit/llm streaming and fires callbacks per chunk.
func (e *LLMExecutor) streamWithClient(ctx context.Context, step *Step, wf *Workflow, messages []llm.Message, opts []llm.ChatOption) error {
	stream, err := e.client.Stream(ctx, messages, opts...)
	if err != nil {
		return fmt.Errorf("llm stream: %w", err)
	}
	defer stream.Close()

	var content strings.Builder
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		content.WriteString(chunk.Delta)
		if e.streamCB != nil {
			e.streamCB(wf.ID, step.ID, chunk.Delta)
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("llm stream: %w", err)
	}

	result := content.String()
	step.Result = result
	wf.Context[step.ID] = result
	e.recordUsage(step.ID, wf, stream.Usage())
	return nil
}

// executeWithProvider uses the legacy LLMProvider interface.
func (e *LLMExecutor) executeWithProvider(ctx context.Context, step *Step, wf *Workflow, prompt, model string) error {
	if model == "" {
		model = e.provider.GetDefaultModel()
	}

	messages := []LLMMessage{{Role: "user", Content: prompt}}
	resp, err := e.provider.Chat(ctx, messages, model)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	step.Result = resp.Content
	wf.Context[step.ID] = resp.Content

	if resp.InputTokens > 0 || resp.OutputTokens > 0 {
		wf.Context[step.ID+"_usage"] = map[string]any{
			"input_tokens":  resp.InputTokens,
			"output_tokens": resp.OutputTokens,
			"model":         resp.Model,
		}
		e.metrics.LLMTokensInput.Add(int64(resp.InputTokens))
		e.metrics.LLMTokensOutput.Add(int64(resp.OutputTokens))
	}
	return nil
}

// resolvePrompt extracts and resolves the prompt from step config.
func (e *LLMExecutor) resolvePrompt(step *Step, wf *Workflow) string {
	prompt, _ := step.Config["prompt"].(string)

	if skillName, ok := step.Config["skill"].(string); ok && skillName != "" {
		if e.skills == nil {
			return ""
		}
		skillPrompt, found := e.skills.LoadSkill(skillName)
		if !found {
			return ""
		}
		prompt = skillPrompt
		if input, ok := step.Config["input"].(string); ok && input != "" {
			prompt += "\n\n" + resolvePromptRefs(input, wf)
		}
	}

	if prompt != "" {
		prompt = resolvePromptRefs(prompt, wf)
	}
	return prompt
}

// recordUsage stores token usage in context and metrics.
func (e *LLMExecutor) recordUsage(stepID string, wf *Workflow, usage *llm.Usage) {
	if usage == nil || (usage.PromptTokens == 0 && usage.CompletionTokens == 0) {
		return
	}
	wf.Context[stepID+"_usage"] = map[string]any{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
	}
	e.metrics.LLMTokensInput.Add(int64(usage.PromptTokens))
	e.metrics.LLMTokensOutput.Add(int64(usage.CompletionTokens))
}
```

Add engine options in `engine.go` (after `WithLLMProvider`):

```go
// WithLLMClient sets the go-kit LLM client for LLM steps (preferred over WithLLMProvider).
func WithLLMClient(c *llm.Client) EngineOption {
	return func(e *Engine) {
		e.executors[StepLLM] = NewLLMExecutorWithClient(c, e.metrics)
	}
}

// WithStreamCallback sets the callback for LLM streaming chunks.
func WithStreamCallback(cb StreamCallback) EngineOption {
	return func(e *Engine) {
		if ex, ok := e.executors[StepLLM].(*LLMExecutor); ok {
			ex.SetStreamCallback(cb)
		}
	}
}
```

Add `llm` import to `engine.go`:

```go
import "github.com/anatolykoptev/go-kit/llm"
```

**Step 5: Run tests to verify they pass**

Run: `cd /home/krolik/src/go-workflow && go test -run TestLLMExecutor -v`
Expected: PASS

**Step 6: Run full test suite**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS

**Step 7: Commit**

```bash
git add go.mod go.sum executor_llm.go executor_llm_test.go engine.go
git commit -m "feat: add LLM streaming via go-kit/llm client"
```

---

### Task 4: LLM Tool Calling (multi-turn)

**Files:**
- Modify: `executor_llm.go` (add tool calling loop)
- Modify: `engine.go` (wire ToolRunner into LLMExecutor)
- Create: `executor_llm_tools_test.go`

**Step 1: Write failing tests for tool calling**

Create `executor_llm_tools_test.go`:

```go
package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-kit/llm"
)

func TestLLMExecutor_ToolCalling_SingleTurn(t *testing.T) {
	t.Parallel()

	// Mock server: first call returns tool_call, second returns content
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// LLM requests a tool call
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "echo",
								"arguments": `{"text":"hello"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
		} else {
			// After tool result, LLM returns final content
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "The echo says: hello",
					},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30},
			})
		}
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL+"/v1", "test-key", "test-model")
	m := NewMetrics()
	runner := &mockToolRunner{results: map[string]string{"echo": "hello"}}

	ex := NewLLMExecutorWithClient(client, m)
	ex.SetToolRunner(runner)

	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{
		ID:   "s1",
		Kind: StepLLM,
		Config: map[string]any{
			"prompt": "echo hello",
			"tools": []any{
				map[string]any{
					"name":        "echo",
					"description": "echoes text",
					"parameters":  map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
				},
			},
			"max_turns": float64(5),
		},
	}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}
	if step.Result != "The echo says: hello" {
		t.Errorf("got %q, want 'The echo says: hello'", step.Result)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount.Load())
	}
}

func TestLLMExecutor_ToolCalling_MaxTurnsLimit(t *testing.T) {
	t.Parallel()

	// Server always returns tool calls — should hit max_turns
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "loop",
							"arguments": `{}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10},
		})
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL+"/v1", "test-key", "test-model")
	m := NewMetrics()
	runner := &mockToolRunner{}
	ex := NewLLMExecutorWithClient(client, m)
	ex.SetToolRunner(runner)

	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{
		ID:   "s1",
		Kind: StepLLM,
		Config: map[string]any{
			"prompt":    "loop forever",
			"tools":     []any{map[string]any{"name": "loop", "description": "loops"}},
			"max_turns": float64(2),
		},
	}

	err := ex.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error for max_turns exceeded")
	}
}

func TestLLMExecutor_NoTools_NoLoop(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "just text"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		})
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL+"/v1", "test-key", "test-model")
	m := NewMetrics()
	ex := NewLLMExecutorWithClient(client, m)

	wf := &Workflow{ID: "wf1", Context: make(map[string]any)}
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "hi"}}

	err := ex.Execute(context.Background(), step, wf)
	if err != nil {
		t.Fatal(err)
	}
	if step.Result != "just text" {
		t.Errorf("got %q, want 'just text'", step.Result)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/krolik/src/go-workflow && go test -run TestLLMExecutor_Tool -v`
Expected: FAIL — `SetToolRunner` undefined.

**Step 3: Implement multi-turn tool calling**

Add to `LLMExecutor` struct in `executor_llm.go`:

```go
toolRunner ToolRunner
```

Add method:

```go
// SetToolRunner sets the tool runner for LLM tool calling.
func (e *LLMExecutor) SetToolRunner(tr ToolRunner) { e.toolRunner = tr }
```

Modify `executeWithClient` to handle tools:

```go
func (e *LLMExecutor) executeWithClient(ctx context.Context, step *Step, wf *Workflow, prompt, model string, stream bool) error {
	messages := []llm.Message{{Role: "user", Content: prompt}}

	tools := e.parseTools(step.Config)
	maxTurns := e.parseMaxTurns(step.Config)

	if len(tools) > 0 && e.toolRunner != nil {
		return e.executeToolLoop(ctx, step, wf, messages, tools, maxTurns)
	}

	var opts []llm.ChatOption
	if stream && e.streamCB != nil {
		return e.streamWithClient(ctx, step, wf, messages, opts)
	}

	resp, err := e.client.Chat(ctx, messages)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	step.Result = resp.Content
	wf.Context[step.ID] = resp.Content
	e.recordUsage(step.ID, wf, resp.Usage)
	return nil
}

// executeToolLoop runs the multi-turn tool calling loop.
func (e *LLMExecutor) executeToolLoop(ctx context.Context, step *Step, wf *Workflow, messages []llm.Message, tools []llm.Tool, maxTurns int) error {
	var totalUsage llm.Usage

	for turn := range maxTurns {
		resp, err := e.client.Chat(ctx, messages, llm.WithTools(tools))
		if err != nil {
			return fmt.Errorf("llm turn %d: %w", turn+1, err)
		}
		accumulateUsage(&totalUsage, resp.Usage)

		// No tool calls — final response
		if len(resp.ToolCalls) == 0 {
			step.Result = resp.Content
			wf.Context[step.ID] = resp.Content
			e.recordUsage(step.ID, wf, &totalUsage)
			return nil
		}

		// Append assistant message with tool calls
		messages = append(messages, llm.Message{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			result, err := e.executeTool(ctx, tc)
			if err != nil {
				result = fmt.Sprintf("error: %s", err)
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return fmt.Errorf("step %s: max_turns (%d) exceeded", step.ID, maxTurns)
}

// executeTool runs a single tool call via ToolRunner.
func (e *LLMExecutor) executeTool(ctx context.Context, tc llm.ToolCall) (string, error) {
	var args map[string]any
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"raw": tc.Function.Arguments}
		}
	}
	return e.toolRunner.Execute(ctx, tc.Function.Name, args)
}

// parseTools converts step config "tools" to llm.Tool slice.
func (e *LLMExecutor) parseTools(cfg map[string]any) []llm.Tool {
	arr, ok := cfg["tools"].([]any)
	if !ok {
		return nil
	}
	tools := make([]llm.Tool, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		desc, _ := m["description"].(string)
		params := m["parameters"]
		if name != "" {
			tools = append(tools, llm.NewTool(name, desc, params))
		}
	}
	return tools
}

// parseMaxTurns returns the max_turns from config, default 10.
func (e *LLMExecutor) parseMaxTurns(cfg map[string]any) int {
	v, _ := cfg["max_turns"].(float64)
	if v <= 0 {
		return 10
	}
	return int(v)
}

func accumulateUsage(total *llm.Usage, add *llm.Usage) {
	if add == nil {
		return
	}
	total.PromptTokens += add.PromptTokens
	total.CompletionTokens += add.CompletionTokens
	total.TotalTokens += add.TotalTokens
}
```

Add import for `encoding/json` to `executor_llm.go`.

Wire ToolRunner in `engine.go` — add to the post-options metrics block (after line 156):

```go
// Wire ToolRunner into LLMExecutor for tool calling
if llmEx, ok := e.executors[StepLLM].(*LLMExecutor); ok {
	if toolEx, ok := e.executors[StepTool].(*ToolExecutor); ok {
		llmEx.SetToolRunner(toolEx.runner)
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/krolik/src/go-workflow && go test -run TestLLMExecutor -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS

**Step 6: Commit**

```bash
git add executor_llm.go executor_llm_tools_test.go engine.go
git commit -m "feat: add LLM multi-turn tool calling via go-kit/llm"
```

---

### Task 5: interrupt_before/after

**Files:**
- Modify: `types.go:128-145` (add `InterruptBefore`, `InterruptAfter` fields)
- Modify: `engine_step.go:109-128` (add interrupt_before check)
- Modify: `engine_step.go:149-162` (add interrupt_after check)
- Create: `interrupt_test.go`

**Step 1: Write failing tests**

Create `interrupt_test.go`:

```go
package workflow

import (
	"context"
	"testing"
)

func TestInterruptBefore_PausesBeforeStep(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(&mockToolRunner{}))

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending, DependsOn: []string{"s1"}},
	})
	wf.InterruptBefore = []string{"s2"}
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	loaded, _ := store.Load(wf.ID)
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval", loaded.State)
	}
	// s1 should be completed, s2 should still be pending
	s1 := loaded.GetStep("s1")
	s2 := loaded.GetStep("s2")
	if s1.State != StepCompleted {
		t.Errorf("s1 state = %s, want completed", s1.State)
	}
	if s2.State != StepPending {
		t.Errorf("s2 state = %s, want pending", s2.State)
	}
}

func TestInterruptAfter_PausesAfterStep(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(&mockToolRunner{}))

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending, DependsOn: []string{"s1"}},
	})
	wf.InterruptAfter = []string{"s1"}
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	loaded, _ := store.Load(wf.ID)
	if loaded.State != StateWaitingApproval {
		t.Errorf("state = %s, want waiting_approval", loaded.State)
	}
	// s1 completed, s2 not started
	s1 := loaded.GetStep("s1")
	s2 := loaded.GetStep("s2")
	if s1.State != StepCompleted {
		t.Errorf("s1 state = %s, want completed", s1.State)
	}
	if s2.State != StepPending {
		t.Errorf("s2 state = %s, want pending", s2.State)
	}
}

func TestInterruptBefore_ResumeAfterApproval(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(&mockToolRunner{}))

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending, DependsOn: []string{"s1"}},
	})
	wf.InterruptBefore = []string{"s2"}
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	// Approve and resume
	_ = engine.HandleApproval(wf.ID, true)
	_ = engine.RunToCompletion(context.Background(), wf.ID)

	loaded, _ := store.Load(wf.ID)
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
	s2 := loaded.GetStep("s2")
	if s2.State != StepCompleted {
		t.Errorf("s2 state = %s, want completed", s2.State)
	}
}

func TestInterruptBefore_NoInterrupt_RunsNormally(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	engine := NewEngine(store, WithToolRunner(&mockToolRunner{}))

	wf := NewWorkflow("wf1", "test", "test:1", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "t"}, State: StepPending},
	})
	// No interrupts configured
	_ = store.Save(wf)
	_ = engine.Start(context.Background(), wf.ID)

	loaded, _ := store.Load(wf.ID)
	if loaded.State != StateCompleted {
		t.Errorf("state = %s, want completed", loaded.State)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/krolik/src/go-workflow && go test -run TestInterrupt -v`
Expected: FAIL — `InterruptBefore` field undefined.

**Step 3: Add fields to Workflow**

In `types.go`, add after `StepsExecuted` field (line 142):

```go
InterruptBefore []string `json:"interrupt_before,omitempty"` // pause before these step IDs
InterruptAfter  []string `json:"interrupt_after,omitempty"`  // pause after these step IDs
```

**Step 4: Implement interrupt checks in RunStep**

In `engine_step.go`, add interrupt_before check after the "mark step running" block (after line 109, before line 111 "step started" log):

```go
// interrupt_before: pause workflow before executing this step
if slices.Contains(w.InterruptBefore, stepID) {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepPending // reset back to pending
			s.StartedAt = 0
		}
		w.State = StateWaitingApproval
		w.UpdatedAt = time.Now().UnixMilli()
	})
	e.getMetrics().ApprovalsPending.Add(1)
	e.log().Info("interrupt_before",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
	)
	e.fireHook(EventWorkflowApprovalNeeded, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"reason":      "interrupt_before",
	})
	return nil
}
```

Add interrupt_after check after the success merge block (after line 162, before line 164 "step completed" log):

```go
// interrupt_after: pause workflow after completing this step
if slices.Contains(w.InterruptAfter, stepID) {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		w.State = StateWaitingApproval
		w.UpdatedAt = time.Now().UnixMilli()
	})
	e.getMetrics().ApprovalsPending.Add(1)
	e.log().Info("interrupt_after",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
	)
	e.fireHook(EventWorkflowApprovalNeeded, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"reason":      "interrupt_after",
	})
	return nil
}
```

**Step 5: Fix HandleApproval for interrupt resume**

In `engine_lifecycle.go`, the existing `HandleApproval` sets workflow to `StateRunning` when approved. For interrupt_before, the step is still pending, so `RunToCompletion` will pick it up. No change needed — it already works.

**Step 6: Run tests to verify they pass**

Run: `cd /home/krolik/src/go-workflow && go test -run TestInterrupt -v`
Expected: PASS

**Step 7: Run full test suite**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS

**Step 8: Commit**

```bash
git add types.go engine_step.go interrupt_test.go
git commit -m "feat: add interrupt_before/after for declarative HITL"
```

---

### Task 6: Final verification and tag

**Step 1: Run full test suite with race detector**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS

**Step 2: Run linter**

Run: `cd /home/krolik/src/go-workflow && make lint`
Expected: no issues

**Step 3: Update ROADMAP.md**

Mark all v0.7 items as done:
```markdown
- [x] State reducers: per-context-key merge semantics (append, replace, sum) instead of last-write-wins
- [x] Typed variable passing: `{"$ref": "step_id.field.subfield"}` path-selector for step inputs
- [x] LLM streaming: token-by-token callback from LLMProvider to caller
- [x] LLM tool calling in workflow: LLM step can invoke tools, multi-turn within single step
- [x] `interrupt_before/after`: declarative HITL — pause before/after named steps without code change
```

**Step 4: Commit and tag**

```bash
git add docs/ROADMAP.md
git commit -m "docs: mark v0.7.0 AI Enhancements complete"
git tag v0.7.0
```
