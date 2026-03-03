package workflow

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

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

// ResolveRefs replaces {{stepID}} placeholders in a string with workflow context values.
// Also resolves ${VAR} and $env.VAR patterns with environment variables (n8n compat).
func ResolveRefs(s string, wf *Workflow) string {
	for id, val := range wf.Context {
		s = strings.ReplaceAll(s, "{{"+id+"}}", fmt.Sprintf("%v", val))
	}
	// Resolve ${VAR} environment variable references (n8n compat)
	s = resolveEnvVars(s)
	return s
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
