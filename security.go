package workflow

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
)

// SecurityPolicy defines execution limits and constraints for a workflow.
type SecurityPolicy struct {
	// MaxSteps is the maximum total steps a workflow can execute (including retries).
	// 0 = unlimited (default: 100).
	MaxSteps int `json:"max_steps,omitempty"`

	// MaxDuration is the maximum wall-clock time for the entire workflow.
	// 0 = unlimited (default: 30 minutes).
	MaxDuration time.Duration `json:"max_duration_ms,omitempty"`

	// MaxStepDuration is the maximum time for a single step execution.
	// 0 = unlimited (default: 10 minutes).
	MaxStepDuration time.Duration `json:"max_step_duration_ms,omitempty"`

	// AllowedTools restricts which tools can be called. Empty = all allowed.
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// DeniedTools explicitly blocks specific tools. Takes precedence over AllowedTools.
	DeniedTools []string `json:"denied_tools,omitempty"`

	// AllowShell controls whether the exec/shell tool can be used.
	AllowShell bool `json:"allow_shell,omitempty"`

	// AllowNetwork controls whether HTTP request tools can be used.
	AllowNetwork bool `json:"allow_network,omitempty"`

	// SecretPatterns are regex patterns for values that should be masked in logs/results.
	SecretPatterns []string `json:"secret_patterns,omitempty"`
}

// DefaultSecurityPolicy returns a reasonable default policy.
func DefaultSecurityPolicy() SecurityPolicy {
	return SecurityPolicy{
		MaxSteps:        100,
		MaxDuration:     30 * time.Minute,
		MaxStepDuration: 10 * time.Minute,
		AllowShell:      true,
		AllowNetwork:    true,
	}
}

// Validate checks the policy for consistency.
func (p SecurityPolicy) Validate() error {
	if p.MaxSteps < 0 {
		return errors.New("max_steps must be >= 0")
	}
	if p.MaxDuration < 0 {
		return errors.New("max_duration must be >= 0")
	}
	return nil
}

// IsToolAllowed checks if a tool name is permitted by this policy.
func (p SecurityPolicy) IsToolAllowed(toolName string) bool {
	// Check denied list first (takes precedence)
	for _, denied := range p.DeniedTools {
		if strings.EqualFold(denied, toolName) {
			return false
		}
	}

	// Shell restriction
	if !p.AllowShell && (toolName == "exec" || toolName == "shell") {
		return false
	}

	// Network restriction
	if !p.AllowNetwork && (toolName == ToolHTTPRequest || toolName == "web_fetch" || toolName == "web_search") {
		return false
	}

	// If allowed list is set, tool must be in it
	if len(p.AllowedTools) > 0 {
		for _, allowed := range p.AllowedTools {
			if strings.EqualFold(allowed, toolName) {
				return true
			}
		}
		return false
	}

	return true
}

// --- Secrets masking ---

// secretMaskRegex matches common secret patterns in text.
var defaultSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|apikey|secret|token|password|passwd|auth|bearer)\s*[:=]\s*["']?([^\s"',}{]{8,})["']?`),
	regexp.MustCompile(`(?i)(sk-[a-zA-Z0-9]{20,})`),              // OpenAI keys
	regexp.MustCompile(`(?i)(ghp_[a-zA-Z0-9]{36,})`),             // GitHub tokens
	regexp.MustCompile(`(?i)(xoxb-[a-zA-Z0-9-]+)`),               // Slack tokens
	regexp.MustCompile(`(?i)(AIza[a-zA-Z0-9_-]{35})`),            // Google API keys
	regexp.MustCompile(`(?i)Basic\s+([A-Za-z0-9+/=]{20,})`),      // Basic auth
	regexp.MustCompile(`(?i)Bearer\s+([A-Za-z0-9._~+/=-]{20,})`), // Bearer tokens
}

// MaskSecrets replaces detected secrets in text with [REDACTED].
func MaskSecrets(text string) string {
	for _, re := range defaultSecretPatterns {
		text = re.ReplaceAllStringFunc(text, func(match string) string {
			// Keep the label part, mask the value
			parts := re.FindStringSubmatch(match)
			if len(parts) >= 3 { //nolint:mnd // regex submatch count
				return parts[1] + ":[REDACTED]"
			}
			if len(parts) >= 2 {
				return "[REDACTED]"
			}
			return match
		})
	}
	return text
}

// MaskSecretsInMap recursively masks secrets in a map's string values.
func MaskSecretsInMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			result[k] = MaskSecrets(val)
		case map[string]any:
			result[k] = MaskSecretsInMap(val)
		default:
			result[k] = v
		}
	}
	return result
}

// ResolveSecretRef resolves a secret reference like $SECRET{name} from environment.
// Returns the original string if the reference is not found.
func ResolveSecretRef(s string) string {
	re := regexp.MustCompile(`\$SECRET\{(\w+)\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		groups := re.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if val := os.Getenv(groups[1]); val != "" {
			return val
		}
		return match
	})
}
