package workflow

import (
	"context"
	"errors"
	"math"
	"testing"
)

func floatNearlyEqual(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

// 1. EstimateUSD coverage for default models + unknown.
func TestEstimateUSD(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		model        string
		inputTokens  int64
		outputTokens int64
		want         float64
	}{
		{"haiku", "claude-haiku-4-5", 1000, 1000, 0.001 + 0.005},
		{"sonnet", "claude-sonnet-4-6", 1000, 1000, 0.003 + 0.015},
		{"opus47", "claude-opus-4-7", 1000, 1000, 0.015 + 0.075},
		{"flash", "gemini-2.5-flash", 1000, 1000, 0.0001 + 0.0004},
		{"flash-lite", "gemini-2.5-flash-lite", 1000, 1000, 0.00005 + 0.0002},
		{"pro", "gemini-2.5-pro", 1000, 1000, 0.00125 + 0.005},
		{"unknown", "no-such-model-xyz", 1000, 1000, 0},
		{"haiku-half", "claude-haiku-4-5", 500, 250, 0.001*0.5 + 0.005*0.25},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EstimateUSD(tc.model, tc.inputTokens, tc.outputTokens, DefaultCostModel)
			if !floatNearlyEqual(got, tc.want, 1e-9) {
				t.Errorf("EstimateUSD(%s, %d, %d) = %f, want %f",
					tc.model, tc.inputTokens, tc.outputTokens, got, tc.want)
			}
		})
	}
}

// 2. AddCost aggregates multiple step costs.
func TestWorkflow_AddCost_Aggregates(t *testing.T) {
	t.Parallel()
	wf := NewWorkflow("wf-cost", "n", "o", nil)
	wf.AddCost(StepCost{StepID: "a", Kind: StepLLM, InputTokens: 100, OutputTokens: 50, USDEstimate: 0.001})
	wf.AddCost(StepCost{StepID: "b", Kind: StepLLM, InputTokens: 200, OutputTokens: 100, USDEstimate: 0.003})
	wf.AddCost(StepCost{StepID: "c", Kind: StepImage, Bytes: 12345})

	if wf.Cost == nil {
		t.Fatal("Cost is nil")
	}
	if wf.Cost.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", wf.Cost.InputTokens)
	}
	if wf.Cost.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", wf.Cost.OutputTokens)
	}
	if !floatNearlyEqual(wf.Cost.USDEstimate, 0.004, 1e-9) {
		t.Errorf("USDEstimate = %f, want 0.004", wf.Cost.USDEstimate)
	}
	if wf.Cost.ImagesRendered != 1 {
		t.Errorf("ImagesRendered = %d, want 1", wf.Cost.ImagesRendered)
	}
	if wf.Cost.BytesRendered != 12345 {
		t.Errorf("BytesRendered = %d, want 12345", wf.Cost.BytesRendered)
	}
	if len(wf.Cost.BySteps) != 3 {
		t.Errorf("BySteps len = %d, want 3", len(wf.Cost.BySteps))
	}
	if wf.Cost.BySteps["b"].InputTokens != 200 {
		t.Errorf("BySteps[b].InputTokens = %d, want 200", wf.Cost.BySteps["b"].InputTokens)
	}
}

// 3. recordStepCost without budget never errors.
func TestRecordStepCost_NoBudget(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	eng := NewEngine(store)
	wf := NewWorkflow("wf-nb", "n", "o", nil)
	err := eng.recordStepCost(wf, StepCost{
		StepID: "s1", Kind: StepLLM, Model: "claude-haiku-4-5",
		InputTokens: 1000, OutputTokens: 1000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Cost == nil || wf.Cost.USDEstimate <= 0 {
		t.Errorf("expected USD > 0, got %+v", wf.Cost)
	}
}

// 4. Within budget — no error.
func TestRecordStepCost_WithinBudget(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	eng := NewEngine(store, WithBudget(1.00))
	wf := NewWorkflow("wf-wb", "n", "o", nil)
	err := eng.recordStepCost(wf, StepCost{
		StepID: "s1", Kind: StepLLM, Model: "claude-haiku-4-5",
		InputTokens: 1000, OutputTokens: 1000, // costs 0.006
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 5. Exceeds budget → ErrBudgetExceeded.
func TestRecordStepCost_ExceedsBudget(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	eng := NewEngine(store, WithBudget(0.001))
	wf := NewWorkflow("wf-eb", "n", "o", nil)
	err := eng.recordStepCost(wf, StepCost{
		StepID: "s1", Kind: StepLLM, Model: "claude-opus-4-7",
		InputTokens: 1000, OutputTokens: 1000, // costs 0.090
	})
	if err == nil {
		t.Fatal("expected ErrBudgetExceeded, got nil")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("expected ErrBudgetExceeded wrap, got %v", err)
	}
	// partial cost preserved
	if wf.Cost == nil || wf.Cost.USDEstimate <= 0 {
		t.Errorf("expected partial cost preserved, got %+v", wf.Cost)
	}
}

// 6. LLMExecutor records cost (legacy provider path).
func TestLLMExecutor_RecordsCost(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	provider := &stressLLM{response: &LLMResponse{
		Content: "hi", Model: "claude-haiku-4-5",
		InputTokens: 1000, OutputTokens: 500,
	}}
	eng := NewEngine(store, WithLLMProvider(provider))
	wf := NewWorkflow("wf-llm", "n", "o", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "x"}}
	ex := eng.executors[StepLLM].(*LLMExecutor)
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if wf.Cost == nil {
		t.Fatal("Cost not recorded")
	}
	if wf.Cost.InputTokens != 1000 || wf.Cost.OutputTokens != 500 {
		t.Errorf("tokens: in=%d out=%d", wf.Cost.InputTokens, wf.Cost.OutputTokens)
	}
	want := 0.001*1.0 + 0.005*0.5 // 0.001 + 0.0025 = 0.0035
	if !floatNearlyEqual(wf.Cost.USDEstimate, want, 1e-9) {
		t.Errorf("USDEstimate = %f, want %f", wf.Cost.USDEstimate, want)
	}
	sc, ok := wf.Cost.BySteps["s1"]
	if !ok {
		t.Fatal("BySteps[s1] missing")
	}
	if sc.Kind != StepLLM {
		t.Errorf("Kind = %s, want %s", sc.Kind, StepLLM)
	}
}

// 7. VisionExecutor records cost with Kind=StepVision.
func TestVisionExecutor_RecordsCost(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	p := &fakeVisionProvider{fakeLLMProvider: fakeLLMProvider{
		defaultModel: "claude-sonnet-4-6",
		responses:    []LLMResponse{{Content: "hello", InputTokens: 200, OutputTokens: 100, Model: "claude-sonnet-4-6"}},
	}}
	eng := NewEngine(store, WithVisionProvider(p))
	wf := NewWorkflow("wf-vis", "n", "o", nil)
	step := &Step{ID: "v1", Kind: StepVision, Config: map[string]any{"prompt": "see"}}
	ex := eng.executors[StepVision].(*VisionExecutor)
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if wf.Cost == nil {
		t.Fatal("Cost not recorded")
	}
	sc, ok := wf.Cost.BySteps["v1"]
	if !ok {
		t.Fatal("BySteps[v1] missing")
	}
	if sc.Kind != StepVision {
		t.Errorf("Kind = %s, want %s", sc.Kind, StepVision)
	}
	if sc.InputTokens != 200 || sc.OutputTokens != 100 {
		t.Errorf("tokens: in=%d out=%d", sc.InputTokens, sc.OutputTokens)
	}
}

// 8. ImageExecutor records cost (bytes only, no USD).
func TestImageExecutor_RecordsCost(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	r := &fakeRenderer{response: ImageRenderResult{
		Bytes: make([]byte, 50000), MIMEType: "image/png", SizeBytes: 50000, DurationMS: 33,
	}}
	eng := NewEngine(store, WithImageRenderer(r))
	wf := NewWorkflow("wf-img", "n", "o", nil)
	step := &Step{ID: "i1", Kind: StepImage, Config: map[string]any{"html": "<div/>"}}
	ex := eng.executors[StepImage].(*ImageExecutor)
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if wf.Cost == nil {
		t.Fatal("Cost not recorded")
	}
	if wf.Cost.BytesRendered != 50000 {
		t.Errorf("BytesRendered = %d, want 50000", wf.Cost.BytesRendered)
	}
	if wf.Cost.ImagesRendered != 1 {
		t.Errorf("ImagesRendered = %d, want 1", wf.Cost.ImagesRendered)
	}
	if wf.Cost.USDEstimate != 0 {
		t.Errorf("USDEstimate = %f, want 0", wf.Cost.USDEstimate)
	}
}

// 9. Budget abort: LLM step with cost > budget fails with ErrBudgetExceeded.
func TestBudget_AbortsOnLLMOverflow(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	provider := &stressLLM{response: &LLMResponse{
		Content: "x", Model: "claude-opus-4-7",
		InputTokens: 100000, OutputTokens: 100000,
	}}
	eng := NewEngine(store, WithLLMProvider(provider), WithBudget(0.001))
	wf := NewWorkflow("wf-ovf", "n", "o", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "x"}}
	ex := eng.executors[StepLLM].(*LLMExecutor)
	err := ex.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected ErrBudgetExceeded, got nil")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("expected ErrBudgetExceeded, got %v", err)
	}
}

// 10. Cost metrics increment globally.
func TestCostMetrics_Increment(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	m := NewMetrics()
	provider := &stressLLM{response: &LLMResponse{
		Content: "ok", Model: "claude-haiku-4-5",
		InputTokens: 1000, OutputTokens: 500,
	}}
	eng := NewEngine(store, WithMetrics(m), WithLLMProvider(provider))
	wf := NewWorkflow("wf-m", "n", "o", nil)
	step := &Step{ID: "s1", Kind: StepLLM, Config: map[string]any{"prompt": "x"}}
	ex := eng.executors[StepLLM].(*LLMExecutor)
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if m.WorkflowTokensInputTotal.Load() != 1000 {
		t.Errorf("WorkflowTokensInputTotal = %d, want 1000", m.WorkflowTokensInputTotal.Load())
	}
	if m.WorkflowTokensOutputTotal.Load() != 500 {
		t.Errorf("WorkflowTokensOutputTotal = %d, want 500", m.WorkflowTokensOutputTotal.Load())
	}
	// 0.0035 USD = 0 cents (rounded down from 0.35). Use bigger numbers for cents check.
	provider.response = &LLMResponse{
		Content: "more", Model: "claude-opus-4-7",
		InputTokens: 1000, OutputTokens: 1000, // 0.090 → 9 cents
	}
	step2 := &Step{ID: "s2", Kind: StepLLM, Config: map[string]any{"prompt": "y"}}
	wf2 := NewWorkflow("wf-m2", "n", "o", nil)
	if err := ex.Execute(context.Background(), step2, wf2); err != nil {
		t.Fatalf("Execute2: %v", err)
	}
	if m.WorkflowCostUSDTotal.Load() < 9 {
		t.Errorf("WorkflowCostUSDTotal cents = %d, want >= 9", m.WorkflowCostUSDTotal.Load())
	}

	// Image step → ImagesRendered counter.
	r := &fakeRenderer{response: ImageRenderResult{Bytes: []byte("p"), SizeBytes: 1, MIMEType: "image/png"}}
	eng2 := NewEngine(store, WithMetrics(m), WithImageRenderer(r))
	wf3 := NewWorkflow("wf-m3", "n", "o", nil)
	step3 := &Step{ID: "i1", Kind: StepImage, Config: map[string]any{"html": "<div/>"}}
	imgEx := eng2.executors[StepImage].(*ImageExecutor)
	if err := imgEx.Execute(context.Background(), step3, wf3); err != nil {
		t.Fatalf("Execute3: %v", err)
	}
	if m.WorkflowImagesRenderedTotal.Load() != 1 {
		t.Errorf("WorkflowImagesRenderedTotal = %d, want 1", m.WorkflowImagesRenderedTotal.Load())
	}
}
