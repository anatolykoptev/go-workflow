# go-workflow MCP Tools API — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `RegisterMCPTools()` to go-workflow that registers 6 MCP tools (`wf_create`, `wf_status`, `wf_approve`, `wf_list`, `wf_cancel`, `wf_templates`), so any consumer can expose workflow management via one function call.

**Architecture:** Single file `mcp_tools.go` in root package. Uses existing `Engine`, `WorkflowStore`, `TemplateStore`. `HandleApproval` extended with data passthrough. No new packages, no new deps.

**Tech Stack:** Go 1.26, go-sdk/mcp v1.4.0, go-mcpserver (for tests)

**API patterns** (verified from `executor_mcp_test.go`):
- Handler signature: `func(ctx, *mcp.CallToolRequest, InputStruct) (*mcp.CallToolResult, any, error)`
- Result: `&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: json}}}`
- Tests: `mcpserver.NewTestServer(t, server, cfg)` → HTTP test server with auto-cleanup

---

## File Structure

```
mcp_tools.go            # RegisterMCPTools() + 6 handlers + input/output types
mcp_tools_test.go       # Tests using mcpserver.NewTestServer + MCPToolRunner
engine_recovery.go      # Add HandleApprovalWithData() (~20 lines)
```

---

### Task 1: Add HandleApprovalWithData to engine

**Context:** Current `HandleApproval(id, bool)` stores `"approved"` in context. We need a variant that stores structured `map[string]any` data, so AI can pass results (selected places, written content) into workflow context for downstream steps.

**Files:**
- Modify: `engine_recovery.go` (add function after line ~147)
- Modify: `engine_test.go` (add 2 tests)

- [ ] **Step 1: Write failing tests**

Add to `engine_test.go`:

```go
func TestHandleApprovalWithData(t *testing.T) {
	backend, _ := store.NewSQLiteStore(":memory:")
	s := NewWorkflowStore(backend)
	engine := NewEngine(s)

	wf := NewWorkflow("wf-data", "Test", "test", []Step{
		{ID: "s1", Kind: StepNoop, Config: map[string]any{}},
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}, DependsOn: []string{"s1"}},
	})
	_ = s.Save(wf)
	_ = s.Modify("wf-data", func(w *Workflow) {
		w.State = StateWaitingApproval
		w.Steps[0].State = StepCompleted
	})

	data := map[string]any{"selected": []any{"place1", "place2"}}
	if err := engine.HandleApprovalWithData("wf-data", true, data); err != nil {
		t.Fatalf("HandleApprovalWithData: %v", err)
	}

	loaded, _ := s.Load("wf-data")
	if loaded.State != StateRunning {
		t.Errorf("expected running, got %s", loaded.State)
	}
	ctx, ok := loaded.Context["approve"].(map[string]any)
	if !ok {
		t.Fatalf("expected map in context, got %T", loaded.Context["approve"])
	}
	if ctx["selected"] == nil {
		t.Error("missing selected in context data")
	}
}

func TestHandleApprovalWithData_NilFallback(t *testing.T) {
	backend, _ := store.NewSQLiteStore(":memory:")
	s := NewWorkflowStore(backend)
	engine := NewEngine(s)

	wf := NewWorkflow("wf-nil", "Test", "test", []Step{
		{ID: "approve", Kind: StepApproval, Config: map[string]any{}},
	})
	_ = s.Save(wf)
	_ = s.Modify("wf-nil", func(w *Workflow) { w.State = StateWaitingApproval })

	if err := engine.HandleApprovalWithData("wf-nil", true, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	loaded, _ := s.Load("wf-nil")
	if loaded.Context["approve"] != "approved" {
		t.Errorf("nil data should fall back to 'approved', got %v", loaded.Context["approve"])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/krolik/src/go-workflow && go test -run TestHandleApprovalWithData -v`
Expected: FAIL — `HandleApprovalWithData` not defined

- [ ] **Step 3: Implement**

Add to `engine_recovery.go` after `HandleApproval` (after line ~147):

```go
// HandleApprovalWithData resumes a workflow with structured data from the approver.
// Data is stored in wf.Context[stepID] for downstream steps to consume via $steps.{id}.result.
// If data is nil, falls back to "approved" string (same as HandleApproval).
func (e *Engine) HandleApprovalWithData(workflowID string, approved bool, data map[string]any) error {
	if !approved {
		return e.HandleApproval(workflowID, false)
	}

	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}
	if w.State != StateWaitingApproval {
		return fmt.Errorf("workflow %s is %s, not waiting_approval", workflowID, w.State)
	}

	return e.store.Modify(workflowID, func(w *Workflow) {
		for i := range w.Steps {
			s := &w.Steps[i]
			if s.Kind == StepApproval && s.State == StepPending {
				s.State = StepCompleted
				s.EndedAt = time.Now().UnixMilli()
				if data != nil {
					s.Result = data
					w.Context[s.ID] = data
				} else {
					s.Result = "approved"
					w.Context[s.ID] = "approved"
				}
				break
			}
		}
		if w.CurrentStep != "" {
			w.InterruptBefore = removeString(w.InterruptBefore, w.CurrentStep)
			w.InterruptAfter = removeString(w.InterruptAfter, w.CurrentStep)
		}
		w.State = StateRunning
		w.UpdatedAt = time.Now().UnixMilli()
	})
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/krolik/src/go-workflow && go test -run TestHandleApprovalWithData -v`
Expected: PASS

- [ ] **Step 5: Full suite**

Run: `cd /home/krolik/src/go-workflow && go test ./... -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add engine_recovery.go engine_test.go
git commit -m "feat: add HandleApprovalWithData for structured approval responses"
```

---

### Task 2: Create mcp_tools.go — RegisterMCPTools + 6 handlers

**Context:** Handler signature must match `mcp.AddTool` generic: `func(ctx, *mcp.CallToolRequest, Input) (*mcp.CallToolResult, any, error)`. Result as `&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: json}}}`. See `executor_mcp_test.go:20-25` for verified pattern.

Store.List("") behavior: check if empty string returns all. If not, handle explicitly.

**Files:**
- Create: `mcp_tools.go`

- [ ] **Step 1: Create mcp_tools.go with types + helpers**

```go
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPDeps holds dependencies for workflow MCP tools.
type MCPDeps struct {
	Engine    *Engine
	Templates *TemplateStore
}

// --- Input types ---

type wfCreateInput struct {
	Template string         `json:"template" jsonschema:"Template name,required"`
	Params   map[string]any `json:"params,omitempty" jsonschema:"Template parameters"`
	Owner    string         `json:"owner,omitempty" jsonschema:"Workflow owner (default: mcp)"`
}

type wfStatusInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID,required"`
}

type wfApproveInput struct {
	WorkflowID string         `json:"workflow_id" jsonschema:"Workflow ID,required"`
	Approved   bool           `json:"approved" jsonschema:"Approve (true) or reject (false),required"`
	Data       map[string]any `json:"data,omitempty" jsonschema:"Structured data for workflow context"`
}

type wfListInput struct {
	State string `json:"state,omitempty" jsonschema:"Filter: running, completed, failed, waiting_approval"`
}

type wfCancelInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID,required"`
}

type wfTemplatesInput struct{} // no params

// --- Output types ---

type wfCreateOutput struct {
	WorkflowID string   `json:"workflow_id"`
	Template   string   `json:"template"`
	StepCount  int      `json:"step_count"`
	StepIDs    []string `json:"step_ids"`
}

type wfStepSummary struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type wfStatusOutput struct {
	WorkflowID string          `json:"workflow_id"`
	Name       string          `json:"name"`
	State      string          `json:"state"`
	Steps      []wfStepSummary `json:"steps"`
	Pending    string          `json:"pending_approval,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// textResult marshals v to JSON and wraps in mcp.CallToolResult.
func textResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

// errResult returns an MCP error result.
func errResult(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}, nil, nil
}

// RegisterMCPTools registers workflow management tools on the given MCP server.
func RegisterMCPTools(server *mcp.Server, deps MCPDeps) {
	registerWFCreate(server, deps)
	registerWFStatus(server, deps)
	registerWFApprove(server, deps)
	registerWFList(server, deps)
	registerWFCancel(server, deps)
	registerWFTemplates(server, deps)
}
```

- [ ] **Step 2: Add wf_create handler**

Append to `mcp_tools.go`:

```go
func registerWFCreate(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "wf_create",
		Description: "Create and start a workflow from a template. Returns workflow ID and step list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input wfCreateInput) (*mcp.CallToolResult, any, error) {
		if input.Template == "" {
			return errResult("template is required")
		}
		owner := input.Owner
		if owner == "" {
			owner = "mcp"
		}
		wfID := fmt.Sprintf("wf-%s-%d", input.Template, time.Now().UnixMilli())

		wf, err := deps.Templates.Instantiate(input.Template, wfID, owner, input.Params)
		if err != nil {
			return errResult(fmt.Sprintf("instantiate: %v", err))
		}
		if err := deps.Engine.Store().Save(wf); err != nil {
			return errResult(fmt.Sprintf("save: %v", err))
		}
		if err := deps.Engine.StartAsync(ctx, wfID); err != nil {
			return errResult(fmt.Sprintf("start: %v", err))
		}

		ids := make([]string, len(wf.Steps))
		for i, s := range wf.Steps {
			ids[i] = s.ID
		}
		return textResult(wfCreateOutput{
			WorkflowID: wfID, Template: input.Template,
			StepCount: len(wf.Steps), StepIDs: ids,
		})
	})
}
```

- [ ] **Step 3: Add wf_status handler**

```go
func registerWFStatus(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "wf_status",
		Description: "Get workflow status: state, steps, pending approvals.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input wfStatusInput) (*mcp.CallToolResult, any, error) {
		wf, ok := deps.Engine.Store().Load(input.WorkflowID)
		if !ok {
			return errResult(fmt.Sprintf("workflow %q not found", input.WorkflowID))
		}

		steps := make([]wfStepSummary, len(wf.Steps))
		var pending string
		for i, s := range wf.Steps {
			steps[i] = wfStepSummary{
				ID: s.ID, Kind: string(s.Kind), State: string(s.State), Error: s.Error,
			}
			if s.Kind == StepApproval && s.State == StepPending && wf.State == StateWaitingApproval {
				pending = s.ID
			}
		}
		return textResult(wfStatusOutput{
			WorkflowID: wf.ID, Name: wf.Name, State: string(wf.State),
			Steps: steps, Pending: pending, Error: wf.Error,
		})
	})
}
```

- [ ] **Step 4: Add wf_approve handler**

```go
func registerWFApprove(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "wf_approve",
		Description: "Approve or reject a workflow. Pass data to inject into workflow context.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input wfApproveInput) (*mcp.CallToolResult, any, error) {
		var err error
		if input.Data != nil {
			err = deps.Engine.HandleApprovalWithData(input.WorkflowID, input.Approved, input.Data)
		} else {
			err = deps.Engine.HandleApproval(input.WorkflowID, input.Approved)
		}
		if err != nil {
			return errResult(err.Error())
		}
		if input.Approved {
			deps.Engine.ResumeAsync(ctx, input.WorkflowID)
		}

		wf, _ := deps.Engine.Store().Load(input.WorkflowID)
		state := "unknown"
		if wf != nil {
			state = string(wf.State)
		}
		return textResult(map[string]string{"workflow_id": input.WorkflowID, "state": state})
	})
}
```

- [ ] **Step 5: Add wf_list, wf_cancel, wf_templates**

```go
func registerWFList(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "wf_list",
		Description: "List workflows, optionally filtered by state.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input wfListInput) (*mcp.CallToolResult, any, error) {
		var workflows []*Workflow
		if input.State != "" {
			workflows = deps.Engine.Store().List(WorkflowState(input.State))
		} else {
			// List all non-terminal.
			workflows = append(workflows, deps.Engine.Store().List(StateRunning)...)
			workflows = append(workflows, deps.Engine.Store().List(StateWaitingApproval)...)
			workflows = append(workflows, deps.Engine.Store().List(StatePending)...)
			workflows = append(workflows, deps.Engine.Store().List(StatePaused)...)
		}

		type item struct {
			ID       string `json:"workflow_id"`
			Name     string `json:"name"`
			State    string `json:"state"`
			Template string `json:"template,omitempty"`
		}
		items := make([]item, len(workflows))
		for i, wf := range workflows {
			items[i] = item{ID: wf.ID, Name: wf.Name, State: string(wf.State), Template: wf.TemplateName}
		}
		return textResult(items)
	})
}

func registerWFCancel(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "wf_cancel",
		Description: "Cancel a running or paused workflow.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input wfCancelInput) (*mcp.CallToolResult, any, error) {
		if err := deps.Engine.Cancel(input.WorkflowID); err != nil {
			return errResult(err.Error())
		}
		return textResult(map[string]string{"workflow_id": input.WorkflowID, "state": string(StateCancelled)})
	})
}

func registerWFTemplates(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "wf_templates",
		Description: "List available workflow templates with parameters.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ wfTemplatesInput) (*mcp.CallToolResult, any, error) {
		if deps.Templates == nil {
			return textResult([]wfTemplateInfo{})
		}
		names := deps.Templates.List()
		infos := make([]wfTemplateInfo, 0, len(names))
		for _, name := range names {
			tmpl, ok := deps.Templates.Get(name)
			if !ok {
				continue
			}
			infos = append(infos, wfTemplateInfo{Name: name, Desc: tmpl.Description, Params: tmpl.Params})
		}
		return textResult(infos)
	})
}

type wfTemplateInfo struct {
	Name   string            `json:"name"`
	Desc   string            `json:"description,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}
```

- [ ] **Step 6: Verify compilation**

Run: `cd /home/krolik/src/go-workflow && go build .`
Expected: success

- [ ] **Step 7: Commit**

```bash
git add mcp_tools.go
git commit -m "feat: add RegisterMCPTools — 6 MCP tools for workflow management"
```

---

### Task 3: Tests for MCP tools

**Context:** Use same pattern as `executor_mcp_test.go`: `mcpserver.NewTestServer(t, server, cfg)` creates httptest server. Then use `MCPToolRunner` to call tools — this tests the full MCP round-trip.

**Files:**
- Create: `mcp_tools_test.go`

- [ ] **Step 1: Write tests**

```go
package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-workflow/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func setupMCPToolsTest(t *testing.T) (string, *Engine) {
	t.Helper()
	backend, _ := store.NewSQLiteStore(":memory:")
	s := NewWorkflowStore(backend)
	engine := NewEngine(s)

	// Create temp template.
	dir := t.TempDir()
	tmpl := map[string]any{
		"name": "Test: {{topic}}", "description": "test template",
		"params": map[string]string{"topic": "Topic"},
		"steps": []map[string]any{
			{"id": "s1", "kind": "noop", "config": map[string]any{}},
			{"id": "approve", "kind": "approval", "config": map[string]any{}, "depends_on": []string{"s1"}},
			{"id": "s3", "kind": "noop", "config": map[string]any{}, "depends_on": []string{"approve"}},
		},
	}
	data, _ := json.MarshalIndent(tmpl, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "test-tmpl.json"), data, 0o644)
	ts := NewTemplateStore(dir)

	server := mcp.NewServer(&mcp.Implementation{Name: "wf-test", Version: "0.1"}, nil)
	RegisterMCPTools(server, MCPDeps{Engine: engine, Templates: ts})

	httpServer := mcpserver.NewTestServer(t, server, mcpserver.Config{
		Name: "wf-test", Version: "0.1", DisableRequestLog: true,
	})

	return httpServer.URL + "/mcp", engine
}

func callWFTool(t *testing.T, mcpURL, tool string, args map[string]any) string {
	t.Helper()
	runner := NewMCPToolRunner(map[string]string{"wf": mcpURL})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	result, err := runner.Execute(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	return result
}

func TestMCPTools_Templates(t *testing.T) {
	mcpURL, _ := setupMCPToolsTest(t)
	out := callWFTool(t, mcpURL, "wf_templates", nil)

	var infos []wfTemplateInfo
	if err := json.Unmarshal([]byte(out), &infos); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(infos) != 1 || infos[0].Name != "test-tmpl" {
		t.Errorf("expected [test-tmpl], got %v", infos)
	}
}

func TestMCPTools_CreateAndStatus(t *testing.T) {
	mcpURL, _ := setupMCPToolsTest(t)

	// Create.
	out := callWFTool(t, mcpURL, "wf_create", map[string]any{
		"template": "test-tmpl", "params": map[string]any{"topic": "test"},
	})
	var created wfCreateOutput
	json.Unmarshal([]byte(out), &created)
	if created.StepCount != 3 {
		t.Errorf("expected 3 steps, got %d", created.StepCount)
	}
	if created.WorkflowID == "" {
		t.Fatal("empty workflow_id")
	}

	// Status.
	statusOut := callWFTool(t, mcpURL, "wf_status", map[string]any{
		"workflow_id": created.WorkflowID,
	})
	var status wfStatusOutput
	json.Unmarshal([]byte(statusOut), &status)
	if status.State == "" {
		t.Error("empty state")
	}
}

func TestMCPTools_Cancel(t *testing.T) {
	mcpURL, engine := setupMCPToolsTest(t)

	wf := NewWorkflow("wf-cancel-mcp", "Cancel", "test", []Step{
		{ID: "s1", Kind: StepApproval, Config: map[string]any{}},
	})
	_ = engine.Store().Save(wf)
	_ = engine.StartAsync(context.Background(), "wf-cancel-mcp")

	out := callWFTool(t, mcpURL, "wf_cancel", map[string]any{
		"workflow_id": "wf-cancel-mcp",
	})
	if out == "" {
		t.Fatal("empty cancel response")
	}

	loaded, _ := engine.Store().Load("wf-cancel-mcp")
	if loaded.State != StateCancelled {
		t.Errorf("expected cancelled, got %s", loaded.State)
	}
}

func TestMCPTools_List(t *testing.T) {
	mcpURL, engine := setupMCPToolsTest(t)

	wf := NewWorkflow("wf-list-mcp", "List", "test", []Step{
		{ID: "s1", Kind: StepApproval, Config: map[string]any{}},
	})
	_ = engine.Store().Save(wf)
	_ = engine.StartAsync(context.Background(), "wf-list-mcp")

	out := callWFTool(t, mcpURL, "wf_list", map[string]any{})
	var items []struct{ ID string `json:"workflow_id"` }
	json.Unmarshal([]byte(out), &items)
	found := false
	for _, item := range items {
		if item.ID == "wf-list-mcp" {
			found = true
		}
	}
	if !found {
		t.Error("wf-list-mcp not in list")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `cd /home/krolik/src/go-workflow && go test -run TestMCPTools -v -count=1`
Expected: all PASS

- [ ] **Step 3: Full suite**

Run: `cd /home/krolik/src/go-workflow && go test ./... -count=1`
Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add mcp_tools_test.go
git commit -m "test: add MCP tools integration tests"
```

---

### Task 4: Lint, race test, tag release

- [ ] **Step 1: Lint**

Run: `cd /home/krolik/src/go-workflow && golangci-lint run`
Expected: no issues (fix any)

- [ ] **Step 2: Race test**

Run: `cd /home/krolik/src/go-workflow && go test -race -count=1 ./...`
Expected: all PASS

- [ ] **Step 3: Commit fixes if any**

- [ ] **Step 4: Tag**

```bash
git tag v0.9.0 && git push origin v0.9.0
```

---

## Dependency Graph

```
Task 1 (HandleApprovalWithData) ─┐
                                 ├─→ Task 3 (tests) → Task 4 (release)
Task 2 (mcp_tools.go)           ─┘
```

**Tasks 1 and 2 are parallelizable.**
