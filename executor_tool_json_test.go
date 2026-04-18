package workflow

import "testing"

func TestTryParseJSON_Object(t *testing.T) {
	got := tryParseJSON(`{"session_id":"abc","status":"ok"}`)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if m["session_id"] != "abc" {
		t.Errorf("session_id = %v, want abc", m["session_id"])
	}
}

func TestTryParseJSON_Array(t *testing.T) {
	got := tryParseJSON(`[1,2,3]`)
	arr, ok := got.([]any)
	if !ok {
		t.Fatalf("expected slice, got %T", got)
	}
	if len(arr) != 3 {
		t.Errorf("len = %d, want 3", len(arr))
	}
}

func TestTryParseJSON_PlainString(t *testing.T) {
	got := tryParseJSON("hello world")
	if got != nil {
		t.Errorf("expected nil for plain string, got %v", got)
	}
}

func TestTryParseJSON_Empty(t *testing.T) {
	if tryParseJSON("") != nil {
		t.Error("expected nil for empty")
	}
	if tryParseJSON("x") != nil {
		t.Error("expected nil for single char")
	}
}

func TestTryParseJSON_InvalidJSON(t *testing.T) {
	if tryParseJSON("{not valid json}") != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestToolExecutor_JSONResultTraversal(t *testing.T) {
	// Simulate: step1 returns JSON, step2 references $steps.step1.session_id
	runner := &mockToolRunner{
		results: map[string]string{
			"mock": `{"session_id":"test-123","cookies":{"ct0":"abc"}}`,
		},
	}
	exec := NewToolExecutor(runner)

	wf := NewWorkflow("wf-1", "test", "test", []Step{
		{ID: "step1", Kind: StepTool, Config: map[string]any{
			"tool": "mock", "args": map[string]any{},
		}},
	})
	step := wf.GetStep("step1")
	if err := exec.Execute(t.Context(), step, wf); err != nil {
		t.Fatal(err)
	}

	// step.Result should still be the raw string
	if step.Result != `{"session_id":"test-123","cookies":{"ct0":"abc"}}` {
		t.Errorf("step.Result should be raw string, got %v", step.Result)
	}

	// Context should be parsed map, not string
	ctx := wf.Context["step1"]
	m, ok := ctx.(map[string]any)
	if !ok {
		t.Fatalf("context type = %T, want map[string]any", ctx)
	}
	if m["session_id"] != "test-123" {
		t.Errorf("session_id = %v, want test-123", m["session_id"])
	}

	// Nested path should work via resolvePath
	cookies := resolvePath(ctx, "cookies.ct0")
	if cookies != "abc" {
		t.Errorf("cookies.ct0 = %v, want abc", cookies)
	}
}

func TestToolExecutor_PlainStringResult(t *testing.T) {
	runner := &mockToolRunner{
		results: map[string]string{"echo": "hello world"},
	}
	exec := NewToolExecutor(runner)

	wf := NewWorkflow("wf-2", "test", "test", []Step{
		{ID: "step1", Kind: StepTool, Config: map[string]any{
			"tool": "echo", "args": map[string]any{},
		}},
	})
	step := wf.GetStep("step1")
	if err := exec.Execute(t.Context(), step, wf); err != nil {
		t.Fatal(err)
	}

	// Plain string should stay as string in context
	ctx := wf.Context["step1"]
	s, ok := ctx.(string)
	if !ok {
		t.Fatalf("context type = %T, want string", ctx)
	}
	if s != "hello world" {
		t.Errorf("context value = %q, want %q", s, "hello world")
	}
}
