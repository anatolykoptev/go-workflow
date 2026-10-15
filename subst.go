package workflow

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
)

// Structure-preserving template substitution.
//
// A template step config is parsed FIRST; placeholders are then resolved only
// inside string VALUES of the parsed tree. Keys and structure are never
// touched, so no param value can add, remove or reshape keys. Substituted
// values are never re-scanned (single pass), and ${VAR} environment
// expansion applies only to text the template author wrote — never to a
// caller-supplied value.
//
// Whole-value references: a string value that is exactly "{{name}}" is
// replaced by the typed param value (number, bool, array, object) ONLY when
// the template declares that param with a non-string type (int, bool, float,
// object, array). This is the same rule the previous quoted-literal
// substitution used. For string-typed, untyped or undeclared params the
// reference is replaced by the param's string form, like an embedded one.
// A "{{name}}" embedded in a larger string is always replaced by the string
// form (fmt "%v"). Unknown names are left as literal text.

// placeholderRe matches, in one scan, a ${ENV} reference or a {{name}} reference.
var placeholderRe = regexp.MustCompile(`\$\{(\w+)\}|\{\{([^{}]+)\}\}`)

// wholeRefRe matches a string that is exactly one {{name}} reference.
var wholeRefRe = regexp.MustCompile(`^\{\{([^{}]+)\}\}$`)

// wholeMarkerRe matches a string that is exactly one deprecated typed marker.
var wholeMarkerRe = regexp.MustCompile(`^@@(int|bool|float):(\w+)$`)

// substituter resolves placeholders against merged caller params.
type substituter struct {
	params map[string]any
	specs  ParamsMap
}

// substituteConfig parses raw JSON and substitutes placeholders in string
// values of the parsed tree. Keys are copied unchanged.
func (s substituter) substituteConfig(raw []byte) (any, error) {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	return s.walk(tree)
}

func (s substituter) walk(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			r, err := s.walk(val)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			r, err := s.walk(val)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case string:
		return s.str(x)
	default:
		return v, nil
	}
}

func (s substituter) str(in string) (any, error) {
	if m := wholeMarkerRe.FindStringSubmatch(in); m != nil {
		return s.marker(in, m[1], m[2])
	}
	if m := wholeRefRe.FindStringSubmatch(in); m != nil {
		if typed, ok := s.typedValue(m[1]); ok {
			return typed, nil
		}
	}
	return placeholderRe.ReplaceAllStringFunc(in, func(match string) string {
		g := placeholderRe.FindStringSubmatch(match)
		if g[1] != "" {
			return envOrKeep(g[1], match)
		}
		if val, ok := s.params[g[2]]; ok {
			return fmt.Sprintf("%v", val)
		}
		return match
	}), nil
}

// typedValue returns the JSON-normalised value of a param declared with a
// non-string type; ok is false when the param must be treated as text.
func (s substituter) typedValue(name string) (any, bool) {
	spec, declared := s.specs[name]
	if !declared || spec.Type == "" || spec.Type == ParamTypeString {
		return nil, false
	}
	v, present := s.params[name]
	if !present {
		return nil, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false // not representable as JSON: fall back to text
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// marker resolves a deprecated whole-value @@int:/@@bool:/@@float: marker.
func (s substituter) marker(match, kind, name string) (any, error) {
	warnDeprecatedMarker(match, kind, name)
	v, ok := s.params[name]
	if !ok {
		return nil, fmt.Errorf("ResolveRefs: %s:%s not in context", kind, name)
	}
	lit, err := coerceTyped(kind, v)
	if err != nil {
		return nil, fmt.Errorf("ResolveRefs: %s:%s: %w", kind, name, err)
	}
	var out any
	if err := json.Unmarshal([]byte(lit), &out); err != nil {
		return nil, fmt.Errorf("ResolveRefs: %s:%s: %w", kind, name, err)
	}
	return out, nil
}

func warnDeprecatedMarker(match, kind, name string) {
	warnKey := kind + ":" + name
	if _, already := deprecatedMarkerWarnedSet.LoadOrStore(warnKey, struct{}{}); !already {
		slog.Warn("template: @@type:NAME marker is deprecated; use typed ParamSpec instead",
			"marker", match, "param", name,
			"migration", fmt.Sprintf(`use {"type":"%s"} in params declaration instead of @@%s:%s`, kind, kind, name))
	}
}
