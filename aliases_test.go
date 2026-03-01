package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- NormalizeStepKind ---

func TestNormalizeStepKind(t *testing.T) {
	tests := []struct {
		input StepKind
		want  StepKind
	}{
		// Canonical → unchanged
		{"tool", StepTool},
		{"condition", StepCondition},
		{"agent", StepAgent},
		{"transform", StepTransform},
		{"message", StepMessage},
		{"workflow", StepWorkflow},
		{"llm", StepLLM},
		{"approval", StepApproval},

		// n8n condition aliases
		{"if", StepCondition},
		{"switch", StepCondition},
		{"filter", StepCondition},

		// n8n data transform aliases
		{"set", StepTransform},
		{"merge", StepTransform},

		// n8n code aliases
		{"code", StepAgent},
		{"function", StepAgent},

		// n8n sub-workflow aliases
		{"execute_workflow", StepWorkflow},
		{"sub_workflow", StepWorkflow},

		// n8n messaging aliases
		{"send_message", StepMessage},
		{"telegram", StepMessage},
		{"slack", StepMessage},
		{"notify", StepMessage},

		// n8n HTTP shortcuts
		{ToolHTTPRequest, StepTool},
		{"http", StepTool},

		// Convenience
		{"ask", StepApproval},
		{"confirm", StepApproval},
		{"prompt", StepLLM},

		// Unknown → unchanged
		{"unknown_thing", StepKind("unknown_thing")},
	}

	for _, tt := range tests {
		got := NormalizeStepKind(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeStepKind(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsValidStepKind(t *testing.T) {
	// Canonical kinds
	for _, k := range []StepKind{StepTool, StepLLM, StepApproval, StepCondition, StepMessage, StepWorkflow, StepAgent, StepTransform} {
		if !IsValidStepKind(k) {
			t.Errorf("IsValidStepKind(%q) = false, want true", k)
		}
	}

	// Aliases
	for _, k := range []StepKind{"if", "set", "code", "execute_workflow", "send_message", "ask"} {
		if !IsValidStepKind(k) {
			t.Errorf("IsValidStepKind(%q) = false for alias", k)
		}
	}

	// Invalid
	if IsValidStepKind("totally_bogus") {
		t.Error("IsValidStepKind(totally_bogus) should be false")
	}
}

// --- Template instantiation with aliases ---

func TestTemplateInstantiate_AliasedStepKinds(t *testing.T) {
	dir := t.TempDir()

	// Template using n8n-style kind names
	tmplJSON := `{
  "name": "Alias Test",
  "steps": [
    {"id": "check", "kind": "if", "config": {"check": "{{input}}", "not_empty": true}},
    {"id": "transform", "kind": "set", "config": {"set": {"result": "done"}}, "depends_on": ["check"]},
    {"id": "run_code", "kind": "code", "config": {"task": "process data"}, "depends_on": ["transform"]},
    {"id": "notify", "kind": "send_message", "config": {"content": "finished"}, "depends_on": ["run_code"]},
    {"id": "sub", "kind": "execute_workflow", "config": {"workflow_id": "other"}, "depends_on": ["notify"]}
  ]
}`
	_ = os.WriteFile(filepath.Join(dir, "alias-test.json"), []byte(tmplJSON), 0644)

	store := NewTemplateStore(dir)
	wf, err := store.Instantiate("alias-test", "wf1", "owner", map[string]any{"input": "hello"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify all aliases were normalized
	expected := map[string]StepKind{
		"check":     StepCondition,
		"transform": StepTransform,
		"run_code":  StepAgent,
		"notify":    StepMessage,
		"sub":       StepWorkflow,
	}

	for _, step := range wf.Steps {
		want, ok := expected[step.ID]
		if !ok {
			t.Errorf("unexpected step ID %q", step.ID)
			continue
		}
		if step.Kind != want {
			t.Errorf("step %q: kind = %q, want %q (alias normalization)", step.ID, step.Kind, want)
		}
	}
}

func TestTemplateInstantiate_HTTPAlias(t *testing.T) {
	dir := t.TempDir()

	// Template using "http_request" as step kind (shorthand)
	tmplJSON := `{
  "name": "HTTP Alias",
  "steps": [
    {"id": "fetch", "kind": "http_request", "config": {"url": "https://example.com"}}
  ]
}`
	_ = os.WriteFile(filepath.Join(dir, "http-alias.json"), []byte(tmplJSON), 0644)

	store := NewTemplateStore(dir)
	wf, err := store.Instantiate("http-alias", "wf1", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}

	step := wf.Steps[0]
	if step.Kind != StepTool {
		t.Errorf("kind = %q, want 'tool'", step.Kind)
	}
	// Should have injected implicit tool name
	if step.Config["tool"] != ToolHTTPRequest {
		t.Errorf("config.tool = %v, want 'http_request' (auto-injected)", step.Config["tool"])
	}
}

// --- ValidateWorkflow ---

func TestValidateWorkflow_Valid(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "hello"}},
		{ID: "b", Kind: StepCondition, Config: map[string]any{"not_empty": true}, DependsOn: []string{"a"}},
	})

	errs := e.ValidateWorkflow(wf)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateWorkflow_MissingDep(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "hi"}, DependsOn: []string{"nonexistent"}},
	})

	errs := e.ValidateWorkflow(wf)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if errs[0].StepID != "a" || errs[0].Field != "depends_on" {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidateWorkflow_DuplicateID(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "1"}},
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "2"}},
	})

	errs := e.ValidateWorkflow(wf)
	found := false
	for _, err := range errs {
		if err.Message == "duplicate step ID" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected duplicate ID error, got: %v", errs)
	}
}

func TestValidateWorkflow_Cycle(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "1"}, DependsOn: []string{"b"}},
		{ID: "b", Kind: StepMessage, Config: map[string]any{"content": "2"}, DependsOn: []string{"a"}},
	})

	errs := e.ValidateWorkflow(wf)
	found := false
	for _, err := range errs {
		if containsStr(err.Message, "cycle") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cycle error, got: %v", errs)
	}
}

func TestValidateWorkflow_MissingToolName(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepTool, Config: map[string]any{}}, // missing "tool"
	})

	errs := e.ValidateWorkflow(wf)
	found := false
	for _, err := range errs {
		if err.Field == "config.tool" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing tool error, got: %v", errs)
	}
}

func TestValidateWorkflow_MissingAgentTask(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepAgent, Config: map[string]any{}}, // missing "task"
	})

	errs := e.ValidateWorkflow(wf)
	found := false
	for _, err := range errs {
		if err.Field == "config.task" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing task error, got: %v", errs)
	}
}

func TestValidateWorkflow_InvalidOnErrorBranch(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "hi", "on_error": "ghost_step"}},
	})

	errs := e.ValidateWorkflow(wf)
	found := false
	for _, err := range errs {
		if err.Field == "on_error" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected on_error validation error, got: %v", errs)
	}
}

func TestValidateWorkflow_SelfDependency(t *testing.T) {
	e := newValidationEngine()
	wf := NewWorkflow("w1", "Test", "owner", []Step{
		{ID: "a", Kind: StepMessage, Config: map[string]any{"content": "loop"}, DependsOn: []string{"a"}},
	})

	errs := e.ValidateWorkflow(wf)
	foundSelf := false
	for _, err := range errs {
		if containsStr(err.Message, "depends on itself") {
			foundSelf = true
		}
	}
	if !foundSelf {
		t.Errorf("expected self-dependency error, got: %v", errs)
	}
}

// --- TransformExecutor ---

func TestTransformExecutor_Set(t *testing.T) {
	exec := NewTransformExecutor()
	step := &Step{
		ID:   "t1",
		Kind: StepTransform,
		Config: map[string]any{
			"set": map[string]any{
				"name":     "Alice",
				"score":    42.0,
				"greeting": "Hello, {{prev}}!",
			},
		},
	}
	wf := &Workflow{Context: map[string]any{"prev": "world"}}

	err := exec.Execute(context.TODO(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if result["name"] != "Alice" {
		t.Errorf("name = %v", result["name"])
	}
	if result["greeting"] != "Hello, world!" {
		t.Errorf("greeting = %v, want 'Hello, world!'", result["greeting"])
	}
}

func TestTransformExecutor_PickOmitRename(t *testing.T) {
	exec := NewTransformExecutor()
	step := &Step{
		ID:   "t1",
		Kind: StepTransform,
		Config: map[string]any{
			"input": "source",
			"omit":  []any{"secret"},
			"rename": map[string]any{
				"old_name": "new_name",
			},
		},
	}
	wf := &Workflow{
		Context: map[string]any{
			"source": map[string]any{
				"old_name": "Alice",
				"secret":   "password123",
				"keep":     "this",
			},
		},
	}

	err := exec.Execute(context.TODO(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if _, has := result["secret"]; has {
		t.Error("secret should have been omitted")
	}
	if result["new_name"] != "Alice" {
		t.Errorf("rename failed: new_name = %v", result["new_name"])
	}
	if _, has := result["old_name"]; has {
		t.Error("old_name should have been renamed")
	}
	if result["keep"] != "this" {
		t.Errorf("keep = %v", result["keep"])
	}
}

func TestTransformExecutor_Merge(t *testing.T) {
	exec := NewTransformExecutor()
	step := &Step{
		ID:   "merged",
		Kind: StepTransform,
		Config: map[string]any{
			"merge": []any{"step_a", "step_b"},
		},
	}
	wf := &Workflow{
		Context: map[string]any{
			"step_a": map[string]any{"a": 1},
			"step_b": map[string]any{"b": 2},
		},
	}

	err := exec.Execute(context.TODO(), step, wf)
	if err != nil {
		t.Fatal(err)
	}

	result := step.Result.(map[string]any)
	if result["a"] != 1 || result["b"] != 2 {
		t.Errorf("merge failed: %v", result)
	}
}

// --- n8n compat: Set node → transform step ---

func TestConvertN8n_SetNodeBecomesTransform(t *testing.T) {
	data := []byte(`{
  "name": "Set Test",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "setdata", "name": "Set Data",
      "type": "n8n-nodes-base.set",
      "parameters": {
        "values": {
          "string": [{"name": "status", "value": "active"}],
          "number": [{"name": "count", "value": 42}]
        }
      }
    }
  ],
  "connections": {"Start": {"main": [[{"node": "Set Data", "type": "main", "index": 0}]]}}
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(tmpl.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(tmpl.Steps))
	}

	step := tmpl.Steps[0]
	if step.Kind != StepTransform {
		t.Errorf("kind = %q, want 'transform'", step.Kind)
	}

	var config map[string]any
	_ = json.Unmarshal(step.Config, &config)
	setMap, ok := config["set"].(map[string]any)
	if !ok {
		t.Fatal("config should have 'set' map")
	}
	if setMap["status"] != "active" {
		t.Errorf("set.status = %v, want 'active'", setMap["status"])
	}
}

// --- n8n compat: HTTP Request → http_request tool ---

func TestConvertN8n_GenericHTTPUsesHTTPRequestTool(t *testing.T) {
	data := []byte(`{
  "name": "HTTP Test",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "api", "name": "Call API",
      "type": "n8n-nodes-base.httpRequest",
      "parameters": {
        "method": "POST",
        "url": "https://api.example.com/data",
        "headerParameters": {"parameters": [{"name": "X-Custom", "value": "test"}]},
        "authentication": "bearer"
      }
    }
  ],
  "connections": {"Start": {"main": [[{"node": "Call API", "type": "main", "index": 0}]]}}
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatal(err)
	}

	step := tmpl.Steps[0]
	if step.Kind != StepTool {
		t.Errorf("kind = %q, want 'tool'", step.Kind)
	}

	var config map[string]any
	_ = json.Unmarshal(step.Config, &config)
	if config["tool"] != ToolHTTPRequest {
		t.Errorf("tool = %v, want 'http_request'", config["tool"])
	}

	args, ok := config["args"].(map[string]any)
	if !ok {
		t.Fatal("config should have 'args'")
	}
	if args["method"] != "POST" {
		t.Errorf("method = %v, want POST", args["method"])
	}
	if args["auth_type"] != "bearer" {
		t.Errorf("auth_type = %v, want bearer", args["auth_type"])
	}

	headers, ok := args["headers"].(map[string]any)
	if !ok {
		t.Fatal("args should have 'headers'")
	}
	if headers["X-Custom"] != "test" {
		t.Errorf("header X-Custom = %v, want 'test'", headers["X-Custom"])
	}
}

// --- Security: MaskSecrets ---

func TestMaskSecrets(t *testing.T) {
	tests := []struct {
		input    string
		contains string // should NOT contain this after masking
	}{
		{"api_key: sk-1234567890abcdef1234567890", "sk-1234567890"},
		{"token=ghp_abcdefghijklmnopqrstuvwxyz1234567890", "ghp_abcdefghijklmnopqrstuvwxyz"},
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		{"no secrets here", ""}, // nothing to mask
	}

	for _, tt := range tests {
		result := MaskSecrets(tt.input)
		if tt.contains != "" && containsStr(result, tt.contains) {
			t.Errorf("MaskSecrets(%q) still contains %q: got %q", tt.input, tt.contains, result)
		}
		if containsStr(result, "[REDACTED]") != (tt.contains != "") {
			if tt.contains != "" {
				t.Errorf("MaskSecrets(%q) should contain [REDACTED]", tt.input)
			}
		}
	}
}

// --- SecurityPolicy ---

func TestSecurityPolicy_IsToolAllowed(t *testing.T) {
	// Default policy — all allowed
	p := DefaultSecurityPolicy()
	if !p.IsToolAllowed("anything") {
		t.Error("default policy should allow all tools")
	}

	// Denied list
	p.DeniedTools = []string{"exec"}
	if p.IsToolAllowed("exec") {
		t.Error("exec should be denied")
	}
	if !p.IsToolAllowed("read_file") {
		t.Error("read_file should still be allowed")
	}

	// Allowed list
	p2 := SecurityPolicy{AllowedTools: []string{"read_file", ToolHTTPRequest}}
	if !p2.IsToolAllowed("read_file") {
		t.Error("read_file should be allowed")
	}
	if p2.IsToolAllowed("exec") {
		t.Error("exec should not be allowed (not in allowed list)")
	}

	// Shell restriction
	p3 := SecurityPolicy{AllowShell: false, AllowNetwork: true}
	if p3.IsToolAllowed("exec") {
		t.Error("exec should be blocked when AllowShell=false")
	}
	if !p3.IsToolAllowed(ToolHTTPRequest) {
		t.Error("http_request should be allowed when AllowNetwork=true")
	}

	// Network restriction
	p4 := SecurityPolicy{AllowShell: true, AllowNetwork: false}
	if p4.IsToolAllowed(ToolHTTPRequest) {
		t.Error("http_request should be blocked when AllowNetwork=false")
	}
	if p4.IsToolAllowed("web_fetch") {
		t.Error("web_fetch should be blocked when AllowNetwork=false")
	}
	if !p4.IsToolAllowed("exec") {
		t.Error("exec should be allowed when AllowShell=true")
	}
}

// --- DAG cycle detection ---

func TestDetectCycle_NoCycle(t *testing.T) {
	steps := []Step{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a", "b"}},
	}
	if cycle := detectCycle(steps); cycle != "" {
		t.Errorf("expected no cycle, got: %s", cycle)
	}
}

func TestDetectCycle_SimpleCycle(t *testing.T) {
	steps := []Step{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}
	cycle := detectCycle(steps)
	if cycle == "" {
		t.Error("expected cycle to be detected")
	}
}

func TestDetectCycle_TransitiveCycle(t *testing.T) {
	steps := []Step{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	cycle := detectCycle(steps)
	if cycle == "" {
		t.Error("expected transitive cycle to be detected")
	}
}

// --- helpers ---

func newValidationEngine() *Engine {
	return &Engine{
		executors: map[StepKind]StepExecutor{
			StepTool:      nil,
			StepLLM:       nil,
			StepMessage:   nil,
			StepCondition: nil,
			StepApproval:  nil,
			StepWorkflow:  nil,
			StepAgent:     nil,
			StepTransform: nil,
		},
	}
}
