package workflow

import (
	"context"
	"fmt"
)

// MultiToolRunner routes tool calls to registered runners.
// Runners are checked in registration order; the first match wins.
type MultiToolRunner struct {
	runners []namedRunner
}

type namedRunner struct {
	tools  map[string]bool // known tool names (nil = accepts all as fallback)
	runner ToolRunner
}

// NewMultiToolRunner creates a runner with the given runners registered as fallbacks.
// Fallback runners accept any tool name not matched by earlier runners.
func NewMultiToolRunner(runners ...ToolRunner) *MultiToolRunner {
	m := &MultiToolRunner{}
	for _, r := range runners {
		m.runners = append(m.runners, namedRunner{runner: r})
	}
	return m
}

// Register adds a runner that handles only the specified tools.
// If no tools are specified, the runner is registered as a fallback.
func (m *MultiToolRunner) Register(runner ToolRunner, tools ...string) {
	nr := namedRunner{runner: runner}
	if len(tools) > 0 {
		nr.tools = make(map[string]bool, len(tools))
		for _, t := range tools {
			nr.tools[t] = true
		}
	}
	m.runners = append(m.runners, nr)
}

// Execute routes the tool call to the first matching runner.
func (m *MultiToolRunner) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	for _, nr := range m.runners {
		if nr.tools == nil || nr.tools[name] {
			return nr.runner.Execute(ctx, name, args)
		}
	}
	return "", fmt.Errorf("tool %q: no runner registered", name)
}
