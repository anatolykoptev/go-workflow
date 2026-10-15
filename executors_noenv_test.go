package workflow

import (
	"context"
	"testing"
)

// Runtime sinks: step outputs are external content and instantiated config may
// carry caller-derived text. Neither is ever env-expanded when it flows into
// tool args, messages, prompts or transform output.

const envSentinelName = "WF_RUNTIME_SENTINEL"
const envSentinelValue = "sentinel-secret-value"

func seedSentinelEnv(t *testing.T) *Workflow {
	t.Helper()
	t.Setenv(envSentinelName, envSentinelValue)
	return &Workflow{
		ID:      "wf1",
		Owner:   "chan:123",
		Context: map[string]any{"s1": "x ${" + envSentinelName + "}"},
	}
}

type captureRunner struct{ args map[string]any }

func (c *captureRunner) Execute(_ context.Context, _ string, args map[string]any) (string, error) {
	c.args = args
	return "ok", nil
}

type captureBus struct{ msgs []OutboundMessage }

func (c *captureBus) PublishOutbound(m OutboundMessage) { c.msgs = append(c.msgs, m) }

func TestToolExecutor_ArgsAreNeverEnvExpanded(t *testing.T) {
	wf := seedSentinelEnv(t)
	runner := &captureRunner{}
	step := &Step{ID: "t1", Config: map[string]any{
		"tool": "any",
		"args": map[string]any{
			"embedded": "see {{s1}}",
			"whole":    "{{s1}}",
			"literal":  "${" + envSentinelName + "}",
			"path":     "$steps.s1",
		},
	}}
	if err := NewToolExecutor(runner).Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	want := "x ${" + envSentinelName + "}"
	for k, exp := range map[string]string{
		"embedded": "see " + want,
		"whole":    want,
		"literal":  "${" + envSentinelName + "}",
		"path":     want,
	} {
		if runner.args[k] != exp {
			t.Errorf("args[%q] = %q, want %q (sentinel must stay literal)", k, runner.args[k], exp)
		}
	}
}

func TestMessageExecutor_ContentIsNeverEnvExpanded(t *testing.T) {
	wf := seedSentinelEnv(t)
	bus := &captureBus{}
	step := &Step{ID: "m1", Config: map[string]any{"content": "msg: {{s1}} ${" + envSentinelName + "}"}}
	if err := NewMessageExecutor(bus).Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	want := "msg: x ${" + envSentinelName + "} ${" + envSentinelName + "}"
	if len(bus.msgs) != 1 || bus.msgs[0].Content != want {
		t.Fatalf("published = %#v, want one message %q", bus.msgs, want)
	}
}

func TestLLMExecutor_PromptIsNeverEnvExpanded(t *testing.T) {
	wf := seedSentinelEnv(t)
	prov := &realMockProvider{response: "done"}
	step := &Step{ID: "l1", Config: map[string]any{"prompt": "summarise {{s1}} ${" + envSentinelName + "}"}}
	if err := NewLLMExecutor(prov, NewMetrics()).Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	want := "summarise x ${" + envSentinelName + "} ${" + envSentinelName + "}"
	if prov.lastPrompt != want {
		t.Fatalf("prompt = %q, want %q", prov.lastPrompt, want)
	}
}

func TestTransformExecutor_SetIsNeverEnvExpanded(t *testing.T) {
	wf := seedSentinelEnv(t)
	step := &Step{ID: "x1", Config: map[string]any{
		"set": map[string]any{"k": "{{s1}} ${" + envSentinelName + "}"},
	}}
	if err := NewTransformExecutor().Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	out, _ := step.Result.(map[string]any)
	want := "x ${" + envSentinelName + "} ${" + envSentinelName + "}"
	if out["k"] != want {
		t.Fatalf("set output k = %q, want %q", out["k"], want)
	}
}
