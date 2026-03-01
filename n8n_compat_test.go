package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- ConvertN8nToTemplate ---

func TestConvertN8n_SEOMaintenance(t *testing.T) {
	data := []byte(`{
  "name": "Piter SEO Maintenance",
  "nodes": [
    {
      "parameters": {"rule": {"interval": [{"field": "cronExpression", "expression": "0 3 * * 1"}]}},
      "id": "cron", "name": "Weekly Monday 3am",
      "type": "n8n-nodes-base.scheduleTrigger", "typeVersion": 1, "position": [200, 300]
    },
    {
      "parameters": {
        "method": "POST", "url": "={{ $env.VAELOR_API_URL }}/tools/execute",
        "sendBody": true,
        "bodyParameters": {"parameters": [
          {"name": "tool", "value": "wp_seo_audit"},
          {"name": "args", "value": "{\"scope\": \"all\"}"}
        ]}
      },
      "id": "audit", "name": "SEO Audit",
      "type": "n8n-nodes-base.httpRequest", "typeVersion": 4, "position": [420, 300]
    },
    {
      "parameters": {
        "conditions": {"boolean": [{"value1": "={{ $json.needs_fix }}", "value2": true}]}
      },
      "id": "check", "name": "Has Issues?",
      "type": "n8n-nodes-base.if", "typeVersion": 1, "position": [640, 300]
    },
    {
      "parameters": {
        "chatId": "={{ $env.TELEGRAM_CHAT_ID }}",
        "text": "SEO report: {{ $node[\"SEO Audit\"].json.result }}"
      },
      "id": "notify", "name": "Telegram Report",
      "type": "n8n-nodes-base.telegram", "typeVersion": 1, "position": [860, 300]
    }
  ],
  "connections": {
    "Weekly Monday 3am": {"main": [[{"node": "SEO Audit", "type": "main", "index": 0}]]},
    "SEO Audit": {"main": [[{"node": "Has Issues?", "type": "main", "index": 0}]]},
    "Has Issues?": {"main": [[{"node": "Telegram Report", "type": "main", "index": 0}], []]}
  },
  "tags": [{"name": "seo"}, {"name": "piter.now"}]
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatalf("ConvertN8nToTemplate: %v", err)
	}

	if tmpl.Name != "Piter SEO Maintenance" {
		t.Errorf("name = %q, want 'Piter SEO Maintenance'", tmpl.Name)
	}

	// Should have 3 steps (trigger is skipped)
	if len(tmpl.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(tmpl.Steps))
	}

	// Step 1: SEO Audit — should be a direct tool call (Vaelor API detected)
	audit := tmpl.Steps[0]
	if audit.ID != "audit" {
		t.Errorf("step[0].id = %q, want 'audit'", audit.ID)
	}
	if audit.Kind != StepTool {
		t.Errorf("step[0].kind = %q, want 'tool'", audit.Kind)
	}
	var auditConfig map[string]any
	_ = json.Unmarshal(audit.Config, &auditConfig)
	if auditConfig["tool"] != "wp_seo_audit" {
		t.Errorf("step[0] tool = %v, want 'wp_seo_audit'", auditConfig["tool"])
	}
	// Audit should have NO deps (trigger was removed)
	if len(audit.DependsOn) != 0 {
		t.Errorf("step[0].depends_on = %v, want empty", audit.DependsOn)
	}

	// Step 2: Has Issues? — condition
	check := tmpl.Steps[1]
	if check.Kind != StepCondition {
		t.Errorf("step[1].kind = %q, want 'condition'", check.Kind)
	}
	if len(check.DependsOn) != 1 || check.DependsOn[0] != "audit" {
		t.Errorf("step[1].depends_on = %v, want [audit]", check.DependsOn)
	}

	// Step 3: Telegram Report — message
	notify := tmpl.Steps[2]
	if notify.Kind != StepMessage {
		t.Errorf("step[2].kind = %q, want 'message'", notify.Kind)
	}
	var notifyConfig map[string]any
	_ = json.Unmarshal(notify.Config, &notifyConfig)
	content, _ := notifyConfig["content"].(string)
	if content == "" {
		t.Error("step[2] message content is empty")
	}
	// Should have resolved $node["SEO Audit"] → {{audit}}
	if !containsStr(content, "{{audit}}") {
		t.Errorf("step[2] content should contain {{audit}}, got: %s", content)
	}

	// Trigger params should have cron expression
	if tmpl.Params["cron"] != "0 3 * * 1" {
		t.Errorf("trigger params cron = %q, want '0 3 * * 1'", tmpl.Params["cron"])
	}
}

func TestConvertN8n_VaelorToolDetection(t *testing.T) {
	data := []byte(`{
  "name": "Tool Call Test",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "flush", "name": "Flush Cache",
      "type": "n8n-nodes-base.httpRequest",
      "parameters": {
        "method": "POST", "url": "={{ $env.VAELOR_API_URL }}/tools/execute",
        "sendBody": true,
        "bodyParameters": {"parameters": [
          {"name": "tool", "value": "wp_flush_cache"},
          {"name": "args", "value": "{}"}
        ]}
      }
    }
  ],
  "connections": {
    "Start": {"main": [[{"node": "Flush Cache", "type": "main", "index": 0}]]}
  }
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(tmpl.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (trigger skipped)", len(tmpl.Steps))
	}

	step := tmpl.Steps[0]
	if step.Kind != StepTool {
		t.Errorf("kind = %q, want 'tool'", step.Kind)
	}

	var config map[string]any
	_ = json.Unmarshal(step.Config, &config)
	if config["tool"] != "wp_flush_cache" {
		t.Errorf("tool = %v, want 'wp_flush_cache'", config["tool"])
	}
}

func TestConvertN8n_CodeNode(t *testing.T) {
	data := []byte(`{
  "name": "Code Test",
  "nodes": [
    {"id": "trigger", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "transform", "name": "Filter Data",
      "type": "n8n-nodes-base.code",
      "parameters": {"jsCode": "return items.filter(i => i.json.score > 50);"}
    }
  ],
  "connections": {
    "Start": {"main": [[{"node": "Filter Data", "type": "main", "index": 0}]]}
  }
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(tmpl.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(tmpl.Steps))
	}

	step := tmpl.Steps[0]
	if step.Kind != StepAgent {
		t.Errorf("kind = %q, want 'agent' (code nodes delegate to agent)", step.Kind)
	}

	var config map[string]any
	_ = json.Unmarshal(step.Config, &config)
	task, _ := config["task"].(string)
	if !containsStr(task, "score > 50") {
		t.Errorf("agent task should contain the JS code, got: %s", truncStr(task, 100))
	}
}

func TestConvertN8n_RetryAndContinueOnFail(t *testing.T) {
	data := []byte(`{
  "name": "Retry Test",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "flaky", "name": "Flaky API",
      "type": "n8n-nodes-base.httpRequest",
      "parameters": {"method": "GET", "url": "https://example.com/api"},
      "retryOnFail": true, "maxTries": 3, "waitBetweenTries": 5000,
      "continueOnFail": true
    }
  ],
  "connections": {
    "Start": {"main": [[{"node": "Flaky API", "type": "main", "index": 0}]]}
  }
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatal(err)
	}

	step := tmpl.Steps[0]

	// Check retry
	if len(step.Retry) == 0 {
		t.Fatal("retry should be set")
	}
	var retry map[string]any
	_ = json.Unmarshal(step.Retry, &retry)
	if retry["max"] != float64(3) {
		t.Errorf("retry.max = %v, want 3", retry["max"])
	}
	if retry["delay_ms"] != float64(5000) {
		t.Errorf("retry.delay_ms = %v, want 5000", retry["delay_ms"])
	}

	// Check on_error
	if step.OnError != OnErrorSkip {
		t.Errorf("on_error = %q, want 'skip' (from continueOnFail)", step.OnError)
	}
}

func TestConvertN8n_UnknownNodeFallback(t *testing.T) {
	data := []byte(`{
  "name": "Unknown Test",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "custom", "name": "My Custom Node",
      "type": "n8n-nodes-custom.specialProcessor",
      "parameters": {"mode": "advanced", "threshold": 42}
    }
  ],
  "connections": {
    "Start": {"main": [[{"node": "My Custom Node", "type": "main", "index": 0}]]}
  }
}`)

	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		t.Fatal(err)
	}

	step := tmpl.Steps[0]
	if step.Kind != StepAgent {
		t.Errorf("kind = %q, want 'agent' (unknown nodes should fallback to agent)", step.Kind)
	}

	var config map[string]any
	_ = json.Unmarshal(step.Config, &config)
	task, _ := config["task"].(string)
	if !containsStr(task, "specialProcessor") {
		t.Errorf("agent task should reference the original node type, got: %s", truncStr(task, 100))
	}
}

func TestConvertN8n_EmptyWorkflow(t *testing.T) {
	data := []byte(`{"name": "Empty", "nodes": [], "connections": {}}`)
	_, err := ConvertN8nToTemplate(data)
	if err == nil {
		t.Error("expected error for empty workflow")
	}
}

func TestConvertN8n_OnlyTrigger(t *testing.T) {
	data := []byte(`{
  "name": "Trigger Only",
  "nodes": [{"id": "t", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}}],
  "connections": {}
}`)
	_, err := ConvertN8nToTemplate(data)
	if err == nil {
		t.Error("expected error for workflow with only triggers")
	}
}

// --- TemplateStore n8n loading ---

func TestTemplateStore_LoadsN8nFiles(t *testing.T) {
	dir := t.TempDir()

	// Write a native template
	native := `{"name":"Native","steps":[{"id":"s","kind":"message","config":{"content":"hello"}}]}`
	_ = os.WriteFile(filepath.Join(dir, "native.json"), []byte(native), 0644)

	// Write an n8n template in a subdirectory
	n8nDir := filepath.Join(dir, "n8n")
	_ = os.MkdirAll(n8nDir, 0755)
	n8nJSON := `{
  "name": "N8n Test",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {"id": "msg", "name": "Send Msg", "type": "n8n-nodes-base.telegram", "parameters": {"text": "hello"}}
  ],
  "connections": {"Start": {"main": [[{"node": "Send Msg", "type": "main", "index": 0}]]}}
}`
	_ = os.WriteFile(filepath.Join(n8nDir, "test-wf.n8n.json"), []byte(n8nJSON), 0644)

	store := NewTemplateStore(dir)

	// Should load both
	names := store.List()
	if len(names) != 2 {
		t.Fatalf("templates = %d, want 2 (native + n8n). Got: %v", len(names), names)
	}

	// Check native loads normally
	nativeTmpl, ok := store.Get("native")
	if !ok || nativeTmpl.Name != "Native" {
		t.Errorf("native template not loaded correctly")
	}

	// Check n8n was converted
	n8nTmpl, ok := store.Get("test-wf")
	if !ok {
		t.Fatal("n8n template 'test-wf' not found")
	}
	if n8nTmpl.Name != "N8n Test" {
		t.Errorf("n8n template name = %q, want 'N8n Test'", n8nTmpl.Name)
	}
	if len(n8nTmpl.Steps) != 1 { // trigger skipped, only "Send Msg" remains
		t.Errorf("n8n steps = %d, want 1", len(n8nTmpl.Steps))
	}
}

func TestTemplateStore_InstantiateN8n(t *testing.T) {
	dir := t.TempDir()
	n8nDir := filepath.Join(dir, "n8n")
	_ = os.MkdirAll(n8nDir, 0755)

	n8nJSON := `{
  "name": "SEO Check",
  "nodes": [
    {"id": "start", "name": "Start", "type": "n8n-nodes-base.manualTrigger", "parameters": {}},
    {
      "id": "audit", "name": "Audit",
      "type": "n8n-nodes-base.httpRequest",
      "parameters": {
        "method": "POST", "url": "http://localhost/tools/execute",
        "sendBody": true,
        "bodyParameters": {"parameters": [
          {"name": "tool", "value": "wp_seo_audit"},
          {"name": "args", "value": "{\"scope\": \"all\"}"}
        ]}
      }
    }
  ],
  "connections": {"Start": {"main": [[{"node": "Audit", "type": "main", "index": 0}]]}}
}`
	_ = os.WriteFile(filepath.Join(n8nDir, "seo-check.n8n.json"), []byte(n8nJSON), 0644)

	store := NewTemplateStore(dir)

	wf, err := store.Instantiate("seo-check", "wf1", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}

	if wf.Name != "SEO Check" {
		t.Errorf("workflow name = %q, want 'SEO Check'", wf.Name)
	}

	if len(wf.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(wf.Steps))
	}

	step := wf.Steps[0]
	if step.Kind != StepTool {
		t.Errorf("step kind = %q, want 'tool'", step.Kind)
	}
	if step.Config["tool"] != "wp_seo_audit" {
		t.Errorf("step tool = %v, want 'wp_seo_audit'", step.Config["tool"])
	}
}

// --- Environment variable resolution ---

func TestResolveEnvVars(t *testing.T) {
	t.Setenv("TEST_VAR", "hello")
	t.Setenv("EMPTY_VAR", "")

	tests := []struct {
		input string
		want  string
	}{
		{"${TEST_VAR}", "hello"},
		{"prefix-${TEST_VAR}-suffix", "prefix-hello-suffix"},
		{"${NONEXISTENT_VAR}", "${NONEXISTENT_VAR}"}, // keep if not set
		{"no vars here", "no vars here"},
		{"${TEST_VAR} and ${TEST_VAR}", "hello and hello"},
	}

	for _, tt := range tests {
		got := resolveEnvVars(tt.input)
		if got != tt.want {
			t.Errorf("resolveEnvVars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Expression conversion ---

func TestConvertN8nExpressions(t *testing.T) {
	nameToID := map[string]string{
		"SEO Audit":  "audit",
		"Fix Images": "fix_images",
	}

	tests := []struct {
		input string
		want  string
	}{
		{`$node["SEO Audit"].json.result`, `{{audit}}`},
		{`$node['Fix Images'].json.field`, `{{fix_images}}`},
		{`no expressions`, `no expressions`},
		{``, ``},
	}

	for _, tt := range tests {
		got := convertN8nExpressions(tt.input, nameToID)
		if got != tt.want {
			t.Errorf("convertN8nExpressions(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// helpers

func containsStr(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && (s == sub || len(s) >= len(sub) && searchStr(s, sub))
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
