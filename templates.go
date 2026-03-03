package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Template is a parameterized workflow definition loaded from a JSON file.
// Variables in the form {{key}} in step configs are replaced at instantiation time.
type Template struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"` // param name → description (for documentation)
	Steps       []TemplateStep    `json:"steps"`
	Defaults    map[string]any    `json:"defaults,omitempty"` // default param values
}

// TemplateStep mirrors Step but holds raw JSON config for variable substitution.
type TemplateStep struct {
	ID        string          `json:"id"`
	Kind      StepKind        `json:"kind"`
	Config    json.RawMessage `json:"config"`
	DependsOn []string        `json:"depends_on,omitempty"`
	Retry     json.RawMessage `json:"retry,omitempty"`
	OnError   string          `json:"on_error,omitempty"`
}

// TemplateStore loads and caches workflow templates from a directory.
type TemplateStore struct {
	dir       string
	templates map[string]*Template // filename (without ext) → template
	mu        sync.RWMutex
}

// NewTemplateStore creates a store that loads templates from the given directory.
func NewTemplateStore(dir string) *TemplateStore {
	ts := &TemplateStore{
		dir:       dir,
		templates: make(map[string]*Template),
	}
	ts.loadAll()
	return ts
}

// Get returns a template by name (filename without .json extension).
func (ts *TemplateStore) Get(name string) (*Template, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	t, ok := ts.templates[name]
	return t, ok
}

// List returns all template names.
func (ts *TemplateStore) List() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	names := make([]string, 0, len(ts.templates))
	for name := range ts.templates {
		names = append(names, name)
	}
	return names
}

// Reload reloads all templates from disk.
func (ts *TemplateStore) Reload() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.templates = make(map[string]*Template)
	ts.loadAllLocked()
}

// Instantiate creates a Workflow from a template with the given parameters.
// Parameters replace {{key}} placeholders in step configs.
func (ts *TemplateStore) Instantiate(templateName, workflowID, owner string, params map[string]any) (*Workflow, error) {
	tmpl, ok := ts.Get(templateName)
	if !ok {
		return nil, fmt.Errorf("template %q not found", templateName)
	}

	// Merge defaults with provided params (provided wins)
	merged := make(map[string]any)
	for k, v := range tmpl.Defaults {
		merged[k] = v
	}
	for k, v := range params {
		merged[k] = v
	}

	steps, err := instantiateSteps(tmpl.Steps, merged)
	if err != nil {
		return nil, err
	}

	wf := NewWorkflow(workflowID, tmpl.Name, owner, steps)
	wf.TemplateName = templateName
	wf.Description = tmpl.Description

	// Set idempotency key from params if provided
	if key, ok := merged["idempotency_key"].(string); ok && key != "" {
		wf.IdempotencyKey = key
	}

	// Substitute variables in name/description
	for k, v := range merged {
		wf.Name = strings.ReplaceAll(wf.Name, "{{"+k+"}}", fmt.Sprintf("%v", v))
		wf.Description = strings.ReplaceAll(wf.Description, "{{"+k+"}}", fmt.Sprintf("%v", v))
	}

	return wf, nil
}

// instantiateSteps converts template steps to workflow steps with variable substitution.
func instantiateSteps(templateSteps []TemplateStep, merged map[string]any) ([]Step, error) {
	steps := make([]Step, 0, len(templateSteps))
	for _, ts := range templateSteps {
		s, err := instantiateStep(ts, merged)
		if err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// instantiateStep converts a single template step to a workflow step.
func instantiateStep(ts TemplateStep, merged map[string]any) (Step, error) {
	// Substitute variables in the raw JSON config
	configStr := string(ts.Config)
	for k, v := range merged {
		configStr = strings.ReplaceAll(configStr, "{{"+k+"}}", fmt.Sprintf("%v", v))
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(configStr), &config); err != nil {
		return Step{}, fmt.Errorf("step %s: invalid config after substitution: %w", ts.ID, err)
	}

	// Merge step-level retry/on_error into config (engine reads from config)
	if len(ts.Retry) > 0 {
		retryStr := string(ts.Retry)
		for k, v := range merged {
			retryStr = strings.ReplaceAll(retryStr, "{{"+k+"}}", fmt.Sprintf("%v", v))
		}
		var retryVal any
		if err := json.Unmarshal([]byte(retryStr), &retryVal); err == nil {
			config["retry"] = retryVal
		}
	}
	if ts.OnError != "" {
		config["on_error"] = ts.OnError
	}

	// Normalize step kind aliases (e.g., "if" → "condition", "set" → "transform")
	normalizedKind := NormalizeStepKind(ts.Kind)

	// For aliased HTTP/tool shortcuts, inject implicit tool name if missing
	if ts.Kind != normalizedKind && normalizedKind == StepTool {
		if _, hasTool := config["tool"]; !hasTool {
			config["tool"] = string(ts.Kind)
		}
	}

	return Step{
		ID:        ts.ID,
		Kind:      normalizedKind,
		Config:    config,
		DependsOn: ts.DependsOn,
		State:     StepPending,
	}, nil
}

func (ts *TemplateStore) loadAll() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.loadAllLocked()
}

func (ts *TemplateStore) loadAllLocked() {
	if ts.dir == "" {
		return
	}
	ts.loadDirLocked(ts.dir)
}

// loadDirLocked loads templates from a single directory, recursing into subdirs.
func (ts *TemplateStore) loadDirLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			ts.loadDirLocked(filepath.Join(dir, entry.Name()))
			continue
		}
		ts.loadTemplateFile(filepath.Join(dir, entry.Name()), entry.Name())
	}
}

func (ts *TemplateStore) loadTemplateFile(fullPath, fname string) {
	if strings.HasSuffix(fname, ".n8n.json") {
		ts.loadN8nTemplate(fullPath, fname)
		return
	}
	if filepath.Ext(fname) == ".json" {
		ts.loadNativeTemplate(fullPath, fname)
	}
}

func (ts *TemplateStore) loadN8nTemplate(fullPath, fname string) {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return
	}
	tmpl, err := ConvertN8nToTemplate(data)
	if err != nil {
		return
	}
	ts.templates[strings.TrimSuffix(fname, ".n8n.json")] = tmpl
}

func (ts *TemplateStore) loadNativeTemplate(fullPath, fname string) {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return
	}
	var tmpl Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		return
	}
	ts.templates[strings.TrimSuffix(fname, ".json")] = &tmpl
}
