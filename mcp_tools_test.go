package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// setupMCPToolsTest creates an in-memory SQLite-backed engine with a test template
// and starts a test MCP server with all 6 workflow tools registered.
// Returns the MCP server URL and the engine for direct inspection.
func setupMCPToolsTest(t *testing.T) (mcpURL string, eng *Engine) {
	t.Helper()

	s := newTestStore(t)

	eng = NewEngine(s)

	// Write a test template file: noop → approval → noop
	tmplDir := t.TempDir()
	tmplContent := `{
		"name": "test-tmpl",
		"description": "Test template with 3 steps",
		"steps": [
			{"id": "step1", "kind": "noop", "config": {}},
			{"id": "step2", "kind": "approval", "config": {}, "depends_on": ["step1"]},
			{"id": "step3", "kind": "noop", "config": {}, "depends_on": ["step2"]}
		]
	}`
	if err := os.WriteFile(filepath.Join(tmplDir, "test-tmpl.json"), []byte(tmplContent), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	tmplStore := NewTemplateStore(tmplDir)

	server := mcp.NewServer(&mcp.Implementation{Name: "wf-test", Version: "0.1"}, nil)
	RegisterMCPTools(server, MCPDeps{Engine: eng, Templates: tmplStore})

	ts := mcpserver.NewTestServer(t, server, mcpserver.Config{
		Name:              "wf-test",
		Version:           "0.1",
		DisableRequestLog: true,
	})

	return ts.URL + "/mcp", eng
}

// callWFTool creates a runner, connects, executes a single tool call, then cleans up.
func callWFTool(t *testing.T, mcpURL, toolName string, args map[string]any) string {
	t.Helper()

	runner := NewMCPToolRunner(map[string]string{"wf": mcpURL})
	t.Cleanup(func() { _ = runner.Close() })

	result, err := runner.Execute(context.Background(), toolName, args)
	if err != nil {
		t.Fatalf("execute %s: %v", toolName, err)
	}
	return result
}

func TestMCPTools_Templates(t *testing.T) {
	mcpURL, _ := setupMCPToolsTest(t)

	result := callWFTool(t, mcpURL, "wf_templates", nil)

	if !strings.Contains(result, "test-tmpl") {
		t.Errorf("wf_templates response does not contain %q: %s", "test-tmpl", result)
	}
}

func TestMCPTools_CreateAndStatus(t *testing.T) {
	mcpURL, _ := setupMCPToolsTest(t)

	// Create a workflow from the test template.
	createResult := callWFTool(t, mcpURL, "wf_create", map[string]any{
		"template": "test-tmpl",
		"owner":    "test-user",
		"params":   map[string]any{},
	})

	var created wfCreateOutput
	if err := json.Unmarshal([]byte(createResult), &created); err != nil {
		t.Fatalf("unmarshal wf_create response: %v\nraw: %s", err, createResult)
	}

	if created.ID == "" {
		t.Errorf("wf_create: expected non-empty workflow_id, got: %s", createResult)
	}
	if created.Steps != 3 {
		t.Errorf("wf_create: expected 3 steps, got %d", created.Steps)
	}
	if len(created.StepIDs) != 3 {
		t.Errorf("wf_create: expected 3 step_ids, got %d: %v", len(created.StepIDs), created.StepIDs)
	}

	// Query status of the created workflow.
	statusResult := callWFTool(t, mcpURL, "wf_status", map[string]any{
		"workflow_id": created.ID,
	})

	var status wfStatusOutput
	if err := json.Unmarshal([]byte(statusResult), &status); err != nil {
		t.Fatalf("unmarshal wf_status response: %v\nraw: %s", err, statusResult)
	}

	if status.ID != created.ID {
		t.Errorf("wf_status: ID = %q, want %q", status.ID, created.ID)
	}
	if status.State == "" {
		t.Errorf("wf_status: state is empty")
	}
}

func TestMCPTools_Cancel(t *testing.T) {
	mcpURL, eng := setupMCPToolsTest(t)

	// Create and save a workflow directly, then set state to running so Cancel accepts it.
	wf := NewWorkflow("wf-cancel-test", "cancel test", "user", []Step{
		{ID: "s1", Kind: StepNoop, Config: map[string]any{}, State: StepPending},
	})
	wf.State = StateRunning
	if err := eng.Store().Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cancelResult := callWFTool(t, mcpURL, "wf_cancel", map[string]any{
		"workflow_id": "wf-cancel-test",
	})

	if !strings.Contains(cancelResult, string(StateCancelled)) {
		t.Errorf("wf_cancel: expected %q in response, got: %s", StateCancelled, cancelResult)
	}

	// Verify via store that it's actually cancelled.
	loaded, ok := eng.Store().Load("wf-cancel-test")
	if !ok {
		t.Fatal("workflow not found after cancel")
	}
	if loaded.State != StateCancelled {
		t.Errorf("state = %q, want %q", loaded.State, StateCancelled)
	}
}

func TestMCPTools_List(t *testing.T) {
	mcpURL, eng := setupMCPToolsTest(t)

	// Save a workflow in running state so wf_list picks it up.
	wf := NewWorkflow("wf-list-test", "list test", "user", nil)
	wf.State = StateRunning
	if err := eng.Store().Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	listResult := callWFTool(t, mcpURL, "wf_list", map[string]any{"state": ""})

	if !strings.Contains(listResult, "wf-list-test") {
		t.Errorf("wf_list: expected workflow ID in response, got: %s", listResult)
	}
}
