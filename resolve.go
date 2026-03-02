package workflow

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// resolveRef replaces "$steps.{id}" references with context values.
func resolveRef(val any, wf *Workflow) any {
	s, ok := val.(string)
	if !ok {
		return val
	}

	// "$steps.check.result" -> context["check"]
	if strings.HasPrefix(s, "$steps.") {
		parts := strings.SplitN(s[7:], ".", 2) // strip "$steps."
		stepID := parts[0]
		if v, ok := wf.Context[stepID]; ok {
			return v
		}
		return s
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

// ParseOwner splits "channel:chatID" into parts.
func ParseOwner(owner string) (channel, chatID string) {
	idx := strings.Index(owner, ":")
	if idx <= 0 {
		return "", ""
	}
	return owner[:idx], owner[idx+1:]
}
