package workflow

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// typedMarkerRe matches "@@int:NAME", "@@bool:NAME", "@@float:NAME" patterns
// wrapped in JSON quotes (the surrounding quotes are part of the match so they
// can be stripped during substitution).
var typedMarkerRe = regexp.MustCompile(`"@@(int|bool|float):(\w+)"`)

// deprecatedMarkerWarnedSet deduplicates @@type:NAME deprecation warnings.
// Key: "kind:name" (e.g. "int:delay"). Ensures each unique marker logs at
// most once per process lifetime regardless of how many templates reference it.
var deprecatedMarkerWarnedSet sync.Map

// ResolveRefsErr is like ResolveRefs but also handles typed markers
// @@int:NAME, @@bool:NAME, @@float:NAME. Typed markers are written with
// surrounding quotes in the template (so the JSON file stays valid pre-
// substitution). After substitution the quotes are stripped and the value
// is emitted as a bare typed literal. Returns an error when a marker
// references a missing key or when the value cannot be coerced to the
// requested type.
//
// Classic {{var}} substitution is unchanged and backward compatible.
func ResolveRefsErr(s string, wf *Workflow) (string, error) {
	// 1. Typed markers first — they need quote stripping.
	// Deprecated: prefer typed ParamSpec declarations instead of @@int/@@bool/@@float markers.
	// See: https://github.com/anatolykoptev/go-workflow/blob/main/docs/template.schema.json
	var firstErr error
	s = typedMarkerRe.ReplaceAllStringFunc(s, func(match string) string {
		if firstErr != nil {
			return match
		}
		groups := typedMarkerRe.FindStringSubmatch(match)
		kind, name := groups[1], groups[2]
		warnKey := kind + ":" + name
		if _, alreadyWarned := deprecatedMarkerWarnedSet.LoadOrStore(warnKey, struct{}{}); !alreadyWarned {
			slog.Warn("template: @@type:NAME marker is deprecated; use typed ParamSpec instead",
				"marker", match, "param", name,
				"migration", fmt.Sprintf(`use {"type":"%s"} in params declaration instead of @@%s:%s`, kind, kind, name))
		}
		v, ok := wf.Context[name]
		if !ok {
			firstErr = fmt.Errorf("ResolveRefs: %s:%s not in context", kind, name)
			return match
		}
		converted, err := coerceTyped(kind, v)
		if err != nil {
			firstErr = fmt.Errorf("ResolveRefs: %s:%s: %w", kind, name, err)
			return match
		}
		return converted
	})
	if firstErr != nil {
		return s, firstErr
	}

	// 2. Classic {{var}} string substitution.
	for id, val := range wf.Context {
		s = strings.ReplaceAll(s, "{{"+id+"}}", fmt.Sprintf("%v", val))
	}
	s = resolveEnvVars(s)
	return s, nil
}

// coerceTyped converts v to the bare JSON literal for the requested kind
// (int, bool, float). Returns the literal string without quotes. Returns
// an error when v cannot be represented as that type.
func coerceTyped(kind string, v any) (string, error) {
	switch kind {
	case ParamTypeInt:
		return coerceInt(v)
	case ParamTypeBool:
		return coerceBool(v)
	case ParamTypeFloat:
		return coerceFloat(v)
	}
	return "", fmt.Errorf("unsupported kind %q for value %T", kind, v)
}

func coerceInt(v any) (string, error) {
	switch x := v.(type) {
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatInt(int64(x), 10), nil
	case string:
		if i, err := strconv.ParseInt(x, 10, 64); err == nil {
			return strconv.FormatInt(i, 10), nil
		}
		return "", fmt.Errorf("not int-coercible: %q", x)
	}
	return "", fmt.Errorf("unsupported int source type %T", v)
}

func coerceBool(v any) (string, error) {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x), nil
	case string:
		if b, err := strconv.ParseBool(x); err == nil {
			return strconv.FormatBool(b), nil
		}
		return "", fmt.Errorf("not bool-coercible: %q", x)
	}
	return "", fmt.Errorf("unsupported bool source type %T", v)
}

func coerceFloat(v any) (string, error) {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(x), nil
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64), nil
		}
		return "", fmt.Errorf("not float-coercible: %q", x)
	}
	return "", fmt.Errorf("unsupported float source type %T", v)
}

// resolveRef replaces "$steps.{id}" references with context values.
func resolveRef(val any, wf *Workflow) any {
	s, ok := val.(string)
	if !ok {
		return val
	}

	// "$steps.stepID.path..." -> context["stepID"] with optional nested traversal
	if strings.HasPrefix(s, "$steps.") {
		parts := strings.SplitN(s[7:], ".", 2) // strip "$steps."
		stepID := parts[0]
		v, ok := wf.Context[stepID]
		if !ok {
			return s
		}
		if len(parts) == 2 && parts[1] != "" {
			if resolved := resolvePath(v, parts[1]); resolved != nil {
				return resolved
			}
		}
		return v
	}

	// "{{stepID}}" -> context["stepID"] (whole-value replacement)
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") && strings.Count(s, "{{") == 1 {
		stepID := strings.TrimSpace(s[2 : len(s)-2])
		if v, ok := wf.Context[stepID]; ok {
			return v
		}
	}

	// Inline {{stepID}} references within a larger string
	if strings.Contains(s, "{{") {
		return ResolveRefs(s, wf)
	}

	return s
}

// resolvePromptRefs replaces {{stepID}} placeholders in a string with context values.
func resolvePromptRefs(s string, wf *Workflow) string {
	return ResolveRefs(s, wf)
}

// ResolveRefs replaces {{stepID}} placeholders in a string with workflow
// context values. Also resolves ${VAR} patterns with environment variables
// (n8n compat). Preserves the original string-only signature for backward
// compatibility. Callers that need typed @@int/@@bool/@@float substitution
// should migrate to ResolveRefsErr.
//
// TODO(#typed-markers): switch engine.go call sites to ResolveRefsErr so
// typed-marker errors surface as workflow step failures instead of log warns.
func ResolveRefs(s string, wf *Workflow) string {
	out, err := ResolveRefsErr(s, wf)
	if err != nil {
		// Best-effort fallback — log and continue. Legacy callers expected
		// string-only behavior; typed markers in old templates will surface
		// upstream as JSON-unmarshal errors rather than step failures.
		slog.Warn("ResolveRefs typed-marker error", "err", err)
	}
	return out
}

// envVarRegex matches ${VAR_NAME} patterns.
var envVarRegex = regexp.MustCompile(`\$\{(\w+)\}`)

// resolveEnvVars replaces ${VAR} patterns with os.Getenv values.
// Only replaces if the env var is actually set (non-empty).
func resolveEnvVars(s string) string {
	return envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		groups := envVarRegex.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if val := os.Getenv(groups[1]); val != "" {
			return val
		}
		return match // keep placeholder if env var not set
	})
}

// resolveRefValue resolves {"$ref": "stepID.field.sub"} map patterns.
// If val is a map with a "$ref" key, splits into stepID + path,
// looks up context, and traverses. Otherwise falls back to resolveRef.
func resolveRefValue(val any, wf *Workflow) any {
	m, ok := val.(map[string]any)
	if !ok {
		return resolveRef(val, wf)
	}
	ref, ok := m["$ref"].(string)
	if !ok {
		return val
	}
	parts := strings.SplitN(ref, ".", 2)
	stepID := parts[0]
	root, ok := wf.Context[stepID]
	if !ok {
		return val
	}
	if len(parts) == 2 && parts[1] != "" {
		if resolved := resolvePath(root, parts[1]); resolved != nil {
			return resolved
		}
	}
	return root
}

// resolvePath traverses a nested structure using dot-path notation.
// Supports map field access, array indexing [N], and wildcard [*].
// Returns nil for missing paths or out-of-bounds indices.
func resolvePath(root any, path string) any {
	segments := splitPath(path)
	cur := root
	for _, seg := range segments {
		cur = resolveSegment(cur, seg)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// splitPath splits "a.b[0].c[*].d" into ["a","b","[0]","c","[*]","d"].
func splitPath(path string) []string {
	var segs []string
	for _, part := range strings.Split(path, ".") {
		if idx := strings.Index(part, "["); idx >= 0 {
			if idx > 0 {
				segs = append(segs, part[:idx])
			}
			segs = append(segs, part[idx:])
		} else {
			segs = append(segs, part)
		}
	}
	return segs
}

// resolveSegment resolves one path segment against a value.
//   - "[*]" on []any → returns the whole slice (next segment projects)
//   - "[N]" on []any → array index access
//   - field name on []any → wildcard projection (extract field from each map)
//   - field name on map[string]any → direct field access
func resolveSegment(val any, seg string) any {
	switch v := val.(type) {
	case map[string]any:
		return v[seg] // nil if missing
	case []any:
		return resolveSliceSegment(v, seg)
	default:
		return nil
	}
}

// resolveSliceSegment handles path segment resolution on a slice value.
func resolveSliceSegment(v []any, seg string) any {
	if seg == "[*]" {
		return v
	}
	if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
		inner := seg[1 : len(seg)-1]
		idx, err := strconv.Atoi(inner)
		if err != nil || idx < 0 || idx >= len(v) {
			return nil
		}
		return v[idx]
	}
	return projectField(v, seg)
}

// projectField extracts a named field from each map element in a slice.
func projectField(v []any, field string) any {
	result := make([]any, 0, len(v))
	for _, elem := range v {
		if m, ok := elem.(map[string]any); ok {
			if fv, exists := m[field]; exists {
				result = append(result, fv)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ParseOwner splits "channel:chatID" into parts.
func ParseOwner(owner string) (channel, chatID string) {
	idx := strings.Index(owner, ":")
	if idx <= 0 {
		return "", ""
	}
	return owner[:idx], owner[idx+1:]
}
