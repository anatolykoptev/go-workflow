package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// stepFieldAliases maps n8n-style or alternate JSON keys on a step entry to
// the canonical name used in TemplateStep. Currently only the dependency
// field has a known divergence; future aliases land here.
var stepFieldAliases = map[string]string{
	"depends": "depends_on",
}

// ParseTemplate parses a native go-workflow template from raw JSON bytes.
// It rewrites known step-field aliases (e.g. n8n-style "depends" → "depends_on")
// and then unmarshals strictly — unknown fields cause a clear error instead of
// being silently dropped (which previously masked author typos).
//
// Load-time validation: every ParamSpec.Default is coercion-checked against
// its declared Type. A mismatched default (e.g. type="int", default="hello")
// returns an error immediately so the bug is caught at startup, not at runtime.
// Multiple default mismatches are collected and returned as a single error.
func ParseTemplate(data []byte) (*Template, error) {
	rewritten, err := rewriteStepAliases(data)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(rewritten))
	dec.DisallowUnknownFields()
	var tmpl Template
	if err := dec.Decode(&tmpl); err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	// Load-time validation: check that declared defaults match their param types.
	if errs := validateParamDefaults(tmpl.Params); len(errs) > 0 {
		return nil, fmt.Errorf("parse template %q: param defaults invalid: %w",
			tmpl.Name, errors.Join(errs...))
	}
	return &tmpl, nil
}

// validateParamDefaults checks every ParamSpec that has a non-nil Default
// value can actually be coerced to the declared Type. Returns one error per
// violated param so callers get a full list in a single ParseTemplate call.
func validateParamDefaults(params ParamsMap) []error {
	var errs []error
	for name, spec := range params {
		if spec.Default == nil {
			continue
		}
		if err := checkParamType(spec.Type, spec.Default); err != nil {
			errs = append(errs, fmt.Errorf("param %q (type=%s): default value %v is not %s-coercible: %w",
				name, spec.Type, spec.Default, spec.Type, err))
		}
	}
	return errs
}

// checkParamType returns nil when v is coercible to the declared type,
// or an error explaining the mismatch.
func checkParamType(typ string, v any) error {
	switch typ {
	case ParamTypeInt:
		_, err := coerceInt(v)
		return err
	case ParamTypeBool:
		_, err := coerceBool(v)
		return err
	case ParamTypeFloat:
		_, err := coerceFloat(v)
		return err
	case ParamTypeString:
		// Anything can be formatted as a string; accept all.
		return nil
	case ParamTypeObject:
		if _, ok := v.(map[string]any); ok {
			return nil
		}
		return fmt.Errorf("expected object, got %T", v)
	case ParamTypeArray:
		if _, ok := v.([]any); ok {
			return nil
		}
		return fmt.Errorf("expected array, got %T", v)
	default:
		// Unknown type declared — pass through rather than break existing templates.
		return nil
	}
}

// rewriteStepAliases reads the raw JSON, walks `steps[]`, and renames known
// alias keys per stepFieldAliases. Returns the rewritten JSON. If a step
// already has both the alias and the canonical key, the canonical wins and
// the alias is dropped (the alias would have been ignored anyway).
func rewriteStepAliases(data []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("rewrite aliases: %w", err)
	}
	steps, ok := raw["steps"].([]any)
	if !ok {
		return data, nil
	}
	for _, s := range steps {
		stepMap, ok := s.(map[string]any)
		if !ok {
			continue
		}
		for alias, canonical := range stepFieldAliases {
			v, hasAlias := stepMap[alias]
			if !hasAlias {
				continue
			}
			delete(stepMap, alias)
			if _, hasCanonical := stepMap[canonical]; !hasCanonical {
				stepMap[canonical] = v
			}
		}
	}
	return json.Marshal(raw)
}

// ParamsMap is a map from param name to ParamSpec. It accepts both the legacy
// string form ("name": "description") and the new typed object form
// ("name": {"type": "int", "default": 6}).
type ParamsMap map[string]ParamSpec

// UnmarshalJSON implements json.Unmarshaler. It handles both:
//   - Legacy string value: "paramName": "description text"
//     → ParamSpec{Type: "string", Description: "description text"}
//   - Typed object value: "paramName": {"type": "int", "default": 6}
//     → parsed directly into ParamSpec
func (p *ParamsMap) UnmarshalJSON(data []byte) error {
	// First decode the raw map to detect value types.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(ParamsMap, len(raw))
	for k, v := range raw {
		trimmed := bytes.TrimSpace(v)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// Legacy: string description.
			var desc string
			if err := json.Unmarshal(v, &desc); err != nil {
				return fmt.Errorf("params[%s]: %w", k, err)
			}
			out[k] = ParamSpec{Type: ParamTypeString, Description: desc}
		} else {
			// Typed object form.
			var spec ParamSpec
			if err := json.Unmarshal(v, &spec); err != nil {
				return fmt.Errorf("params[%s]: %w", k, err)
			}
			if spec.Type == "" {
				spec.Type = ParamTypeString
			}
			out[k] = spec
		}
	}
	*p = out
	return nil
}

// paramSpecWire is the full-object JSON form for a ParamSpec.
// Fields with zero values are omitted to keep the output compact.
// This is used by ParamsMap.MarshalJSON for non-legacy entries.
type paramSpecWire struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
}

// MarshalJSON implements json.Marshaler. It preserves the legacy wire shape
// for trivially-legacy entries (Type=string, no default/required/enum) by
// emitting a bare string (the Description). This keeps backward-compat for
// consumers (go-wp, vaelor, krolik-agent) that read params as map[string]string.
//
// Non-trivial entries (typed, required, enumerated, or with a default) are
// marshaled as the full {"type":"...","description":"...",...} object form.
func (p ParamsMap) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(p))
	for k, v := range p {
		var raw json.RawMessage
		var err error
		if v.Type == ParamTypeString && v.Default == nil && !v.Required && len(v.Enum) == 0 {
			// Legacy wire shape: bare string description.
			raw, err = json.Marshal(v.Description)
		} else {
			raw, err = json.Marshal(paramSpecWire(v))
		}
		if err != nil {
			return nil, fmt.Errorf("params[%s]: %w", k, err)
		}
		out[k] = raw
	}
	return json.Marshal(out)
}

// templateVarRe matches {{varname}} placeholders in template step configs.
var templateVarRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// Template is a parameterized workflow definition loaded from a JSON file.
// Variables in the form {{key}} in step configs are replaced at instantiation time.
type Template struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Params      ParamsMap      `json:"params,omitempty"` // param name → ParamSpec (or legacy description string)
	Steps       []TemplateStep `json:"steps"`
	Defaults    map[string]any `json:"defaults,omitempty"` // default param values
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
//
// Before substitution:
//  1. Required params are validated — missing required param → error.
//  2. Non-string typed params are coerced to their declared type — a string
//     "6000" for an int-typed param becomes the integer 6000, so the
//     subsequent "{{wait_ms}}" substitution emits a bare JSON integer.
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

	// Validate required params and coerce typed values before substitution.
	if err := validateAndCoerceParams(tmpl.Params, merged); err != nil {
		return nil, fmt.Errorf("template %q: %w", templateName, err)
	}

	// Lint-level check: scan all step configs for {{var}} references that
	// have no matching entry in the merged map or declared params.
	if err := validateUnknownVars(tmpl, merged); err != nil {
		return nil, fmt.Errorf("template %q: %w", templateName, err)
	}

	steps, err := instantiateSteps(tmpl.Steps, merged, tmpl.Params)
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

// validateAndCoerceParams checks required params and coerces typed values in-place.
// For non-string types, the caller's string value (e.g. "6000") is coerced to
// the native type (int 6000) so that subsequent substitution emits a bare
// JSON literal rather than a quoted string.
func validateAndCoerceParams(specs ParamsMap, merged map[string]any) error {
	var errs []error
	for name, spec := range specs {
		v, present := merged[name]
		if !present || v == nil {
			if spec.Required {
				errs = append(errs, fmt.Errorf("required param %q is missing", name))
			}
			continue
		}
		coerced, err := coerceParamValue(spec.Type, v)
		if err != nil {
			errs = append(errs, fmt.Errorf("param %q (type=%s): %w", name, spec.Type, err))
			continue
		}
		merged[name] = coerced
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// coerceParamValue converts v to the appropriate Go type for the declared param type.
// String params are returned as-is (anything formats to string). For numeric/bool
// params, the value is coerced so the JSON substitution emits bare literals.
func coerceParamValue(typ string, v any) (any, error) {
	switch typ {
	case ParamTypeInt:
		s, err := coerceInt(v)
		if err != nil {
			return nil, err
		}
		// Use strconv.ParseInt to preserve int64 precision.
		// Sscanf("%g") via float64 loses precision for values > 2^53
		// (e.g. Telegram chat_id, Discord snowflake). json.Marshal(int64)
		// emits a bare integer, not scientific notation.
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("coerce %q to int: %w", s, err)
		}
		return n, nil
	case ParamTypeBool:
		s, err := coerceBool(v)
		if err != nil {
			return nil, err
		}
		return s == "true", nil
	case ParamTypeFloat:
		s, err := coerceFloat(v)
		if err != nil {
			return nil, err
		}
		var f float64
		if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
			return nil, err
		}
		return f, nil
	default:
		// string, object, array — pass through unchanged.
		return v, nil
	}
}

// validateUnknownVars scans every step config for {{var}} references and
// checks that each var is either in the merged map (a known param) or
// references a step ID (runtime output reference — resolved after the
// named step completes). Unknown refs are reported as errors.
//
// Step IDs are valid runtime output references: {{stepID}} in a later step's
// config is replaced at runtime with the step's result. These are NOT
// template-time params and must not be flagged.
func validateUnknownVars(tmpl *Template, merged map[string]any) error {
	// Build a set of step IDs for O(1) lookup.
	stepIDs := make(map[string]struct{}, len(tmpl.Steps))
	for _, s := range tmpl.Steps {
		stepIDs[s.ID] = struct{}{}
	}

	var errs []error
	for _, step := range tmpl.Steps {
		matches := templateVarRe.FindAllStringSubmatch(string(step.Config), -1)
		for _, m := range matches {
			varName := m[1]
			if _, ok := merged[varName]; ok {
				continue
			}
			if _, ok := tmpl.Params[varName]; ok {
				continue
			}
			if _, ok := stepIDs[varName]; ok {
				// Runtime step-output reference — resolved after named step completes.
				continue
			}
			errs = append(errs, fmt.Errorf("step %q references unknown param {{%s}}", step.ID, varName))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// instantiateSteps converts template steps to workflow steps with variable substitution.
func instantiateSteps(templateSteps []TemplateStep, merged map[string]any, specs ParamsMap) ([]Step, error) {
	steps := make([]Step, 0, len(templateSteps))
	for _, ts := range templateSteps {
		s, err := instantiateStep(ts, merged, specs)
		if err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// instantiateStep converts a single template step to a workflow step.
// specs is optional (nil for tests that call instantiateStep directly without
// a typed params declaration).
func instantiateStep(ts TemplateStep, merged map[string]any, specs ...ParamsMap) (Step, error) {
	var paramSpecs ParamsMap
	if len(specs) > 0 {
		paramSpecs = specs[0]
	}

	// Pre-substitute: for non-string typed params, replace "{{name}}"
	// (with surrounding JSON quotes) with the bare JSON literal so the
	// resulting JSON is valid typed JSON, not a quoted number/bool.
	configStr := string(ts.Config)
	if paramSpecs != nil {
		configStr = applyTypedSubstitutions(configStr, merged, paramSpecs)
	}

	// Build a stub Workflow with merged params as Context so ResolveRefsErr
	// handles both classic {{x}} substitution and typed @@int/@@bool/@@float
	// markers in a single pass. The inline strings.ReplaceAll loop used here
	// previously bypassed typed-marker logic entirely.
	stub := &Workflow{Context: merged}

	configStr, err := ResolveRefsErr(configStr, stub)
	if err != nil {
		return Step{}, fmt.Errorf("step %s: typed-marker substitution failed: %w", ts.ID, err)
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(configStr), &config); err != nil {
		return Step{}, fmt.Errorf("step %s: invalid config after substitution: %w", ts.ID, err)
	}

	// Merge step-level retry/on_error into config (engine reads from config)
	if len(ts.Retry) > 0 {
		retryStr, err := ResolveRefsErr(string(ts.Retry), stub)
		if err != nil {
			return Step{}, fmt.Errorf("step %s: retry typed-marker substitution failed: %w", ts.ID, err)
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

// applyTypedSubstitutions replaces "{{name}}" (with surrounding JSON quotes)
// with bare JSON literals for non-string typed params. This ensures that e.g.
// "wait_ms": "{{wait_ms}}" in the template becomes "wait_ms": 6000 (number)
// after substitution, rather than the quoted string "6000".
//
// The substitution is keyed on the JSON-quoted form `"{{name}}"` so it only
// fires when the placeholder is the entire JSON string value, not when it
// appears inline in a larger string (e.g. "prefix-{{name}}-suffix" is left
// to the classic ResolveRefs string-replacement pass).
func applyTypedSubstitutions(s string, merged map[string]any, specs ParamsMap) string {
	for name, spec := range specs {
		if spec.Type == ParamTypeString || spec.Type == "" {
			continue
		}
		v, ok := merged[name]
		if !ok {
			continue
		}
		// Encode the coerced value as a bare JSON literal.
		encoded, err := json.Marshal(v)
		if err != nil {
			continue
		}
		// Replace `"{{name}}"` (quoted placeholder) with bare literal.
		placeholder := `"{{` + name + `}}"`
		s = strings.ReplaceAll(s, placeholder, string(encoded))
	}
	return s
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
		slog.Warn("template store: read failed", "path", fullPath, "err", err)
		return
	}
	tmpl, err := ParseTemplate(data)
	if err != nil {
		slog.Warn("template store: parse failed", "path", fullPath, "err", err)
		return
	}
	ts.templates[strings.TrimSuffix(fname, ".json")] = tmpl
}
