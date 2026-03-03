package workflow

import "testing"

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
