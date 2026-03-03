package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestToolLoop_Hard_MalformedJSONArgs(t *testing.T) {
	t.Parallel()
	var capturedArgs map[string]any
	runner := &argsCapture{captured: &capturedArgs, result: "parsed"}

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			_, _ = w.Write(toolCallJSON(tc("c1", "tool", `not json at all`), 10, 5))
			return
		}
		_, _ = w.Write(chatJSON("ok", 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, runner)
	wf, step := toolStep("s1", "go", []any{td("tool")})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	if capturedArgs == nil {
		t.Fatal("args not captured")
	}
	raw, ok := capturedArgs["raw"]
	if !ok || raw != "not json at all" {
		t.Errorf("malformed args fallback = %v, want raw='not json at all'", capturedArgs)
	}
}

func TestToolLoop_Hard_EmptyArgs(t *testing.T) {
	t.Parallel()
	var capturedArgs map[string]any
	runner := &argsCapture{captured: &capturedArgs, result: "ok"}

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			_, _ = w.Write(toolCallJSON(tc("c1", "tool", ``), 10, 5))
			return
		}
		_, _ = w.Write(chatJSON("ok", 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, runner)
	wf, step := toolStep("s1", "go", []any{td("tool")})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	// Empty arguments string → nil args (not empty map)
	if capturedArgs != nil {
		t.Errorf("empty args should produce nil, got %v", capturedArgs)
	}
}

func TestToolLoop_Hard_MaxTurnsExactBoundary(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(toolCallJSON(tc("c1", "ping", `{}`), 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, &mockToolRunner{results: map[string]string{"ping": "pong"}})
	wf, step := toolStep("s1", "go", []any{td("ping")}, map[string]any{"max_turns": float64(1)})

	err := ex.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "max_turns (1)") {
		t.Fatalf("error = %v, want max_turns (1) exceeded", err)
	}
	if n.Load() != 1 {
		t.Errorf("API calls = %d, want exactly 1", n.Load())
	}
	// Tool log must still be stored even on max_turns error
	log, ok := wf.Context["s1_tool_calls"].([]map[string]any)
	if !ok || len(log) != 1 {
		t.Errorf("tool log on exhaustion: got %v", wf.Context["s1_tool_calls"])
	}
}

func TestToolLoop_Hard_NoToolCallsKeyOnCleanExit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatJSON("direct answer", 10, 5))
	}))
	t.Cleanup(srv.Close)

	ex := newToolExec(t, srv.URL, &mockToolRunner{})
	wf, step := toolStep("s1", "go", []any{td("unused")})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatal(err)
	}
	// When LLM answers directly without any tool calls, _tool_calls should NOT exist
	if _, exists := wf.Context["s1_tool_calls"]; exists {
		t.Error("_tool_calls should not exist when no tools were called")
	}
}

func TestParseTools_Hard_EdgeCases(t *testing.T) {
	t.Parallel()
	ex := &LLMExecutor{}
	cases := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{"nil_config", nil, 0},
		{"no_tools_key", map[string]any{"prompt": "x"}, 0},
		{"empty_array", map[string]any{"tools": []any{}}, 0},
		{"non_map_items", map[string]any{"tools": []any{"str", 42, nil}}, 0},
		{"missing_name", map[string]any{"tools": []any{map[string]any{"description": "no name"}}}, 0},
		{"valid_mixed", map[string]any{"tools": []any{
			map[string]any{"name": "a", "description": "ok"},
			"garbage",
			map[string]any{"name": "b"},
		}}, 2},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := ex.parseTools(tt.cfg)
			if len(got) != tt.want {
				t.Errorf("parseTools(%q) = %d tools, want %d", tt.name, len(got), tt.want)
			}
		})
	}
}

func TestParseMaxTurns_Hard_EdgeCases(t *testing.T) {
	t.Parallel()
	ex := &LLMExecutor{}
	cases := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{"nil_config", nil, defaultMaxTurns},
		{"no_key", map[string]any{}, defaultMaxTurns},
		{"zero", map[string]any{"max_turns": float64(0)}, defaultMaxTurns},
		{"negative", map[string]any{"max_turns": float64(-5)}, defaultMaxTurns},
		{"string_type", map[string]any{"max_turns": "10"}, defaultMaxTurns},
		{"valid", map[string]any{"max_turns": float64(7)}, 7},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := ex.parseMaxTurns(tt.cfg)
			if got != tt.want {
				t.Errorf("parseMaxTurns(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}
