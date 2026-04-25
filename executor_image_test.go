package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRenderer is a deterministic in-memory ImageRenderer for tests.
type fakeRenderer struct {
	mu          sync.Mutex
	response    ImageRenderResult
	err         error
	lastRequest ImageRenderRequest
	callCount   int
}

func (f *fakeRenderer) Render(_ context.Context, req ImageRenderRequest) (ImageRenderResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.lastRequest = req
	if f.err != nil {
		return ImageRenderResult{}, f.err
	}
	return f.response, nil
}

func newImageWorkflow(stepCfg map[string]any) (*Workflow, *Step) {
	step := &Step{ID: "render", Kind: StepImage, Config: stepCfg}
	wf := NewWorkflow("wf-img", "image", "telegram:1", []Step{*step})
	return wf, &wf.Steps[0]
}

func TestImageExecutor_HappyPath(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	r := &fakeRenderer{response: ImageRenderResult{
		Bytes:      []byte("PNG-bytes"),
		MIMEType:   "image/png",
		SizeBytes:  9,
		DurationMS: 42,
	}}
	ex := NewImageExecutor(r, m)

	wf, step := newImageWorkflow(map[string]any{"html": "<div>hi</div>"})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out, ok := step.Result.(map[string]any)
	if !ok {
		t.Fatalf("step.Result type = %T, want map[string]any", step.Result)
	}
	if got := out["mime_type"]; got != "image/png" {
		t.Errorf("mime_type = %v, want image/png", got)
	}
	if got := out["bytes_size"]; got != int64(9) {
		t.Errorf("bytes_size = %v, want 9", got)
	}
	if got := out["duration_ms"]; got != int64(42) {
		t.Errorf("duration_ms = %v, want 42", got)
	}
	if got := out["format"]; got != "png" {
		t.Errorf("format = %v, want png", got)
	}
	if got := out["width"]; got != imageDefaultWidth {
		t.Errorf("width = %v, want %d", got, imageDefaultWidth)
	}
	if got := out["height"]; got != imageDefaultHeight {
		t.Errorf("height = %v, want %d", got, imageDefaultHeight)
	}
	if wf.Context["render"] == nil {
		t.Error("wf.Context[render] not set")
	}
	if r.lastRequest.Format != "png" || r.lastRequest.Density != 1 {
		t.Errorf("defaults not applied: %+v", r.lastRequest)
	}
	if m.ImageRendersSuccess.Load() != 1 {
		t.Errorf("ImageRendersSuccess = %d, want 1", m.ImageRendersSuccess.Load())
	}
	if m.ImageBytesTotal.Load() != 9 {
		t.Errorf("ImageBytesTotal = %d, want 9", m.ImageBytesTotal.Load())
	}
}

func TestImageExecutor_ExplicitConfigForwarded(t *testing.T) {
	t.Parallel()
	r := &fakeRenderer{response: ImageRenderResult{Bytes: []byte("x"), MIMEType: "image/webp"}}
	ex := NewImageExecutor(r, NewMetrics())

	wf, step := newImageWorkflow(map[string]any{
		"html":    "<p>x</p>",
		"width":   800,
		"height":  600,
		"format":  "webp",
		"density": 2,
		"fonts":   []any{"Inter", "Roboto"},
	})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := r.lastRequest
	if req.Width != 800 || req.Height != 600 {
		t.Errorf("dimensions not forwarded: %dx%d", req.Width, req.Height)
	}
	if req.Format != "webp" {
		t.Errorf("format = %q, want webp", req.Format)
	}
	if req.Density != 2 {
		t.Errorf("density = %d, want 2", req.Density)
	}
	if len(req.Fonts) != 2 || req.Fonts[0] != "Inter" || req.Fonts[1] != "Roboto" {
		t.Errorf("fonts = %v, want [Inter Roboto]", req.Fonts)
	}
}

func TestImageExecutor_EmptyHTML(t *testing.T) {
	t.Parallel()
	r := &fakeRenderer{}
	ex := NewImageExecutor(r, NewMetrics())
	wf, step := newImageWorkflow(map[string]any{"html": ""})

	err := ex.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error on empty html")
	}
	if !strings.Contains(err.Error(), "missing html") {
		t.Errorf("error = %v, want contains 'missing html'", err)
	}
	if r.callCount != 0 {
		t.Errorf("renderer called %d times despite empty html", r.callCount)
	}
}

func TestImageExecutor_InvalidFormat(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	ex := NewImageExecutor(&fakeRenderer{}, m)
	wf, step := newImageWorkflow(map[string]any{"html": "<x/>", "format": "bmp"})

	err := ex.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error on invalid format")
	}
	if !strings.Contains(err.Error(), "png, jpeg, webp, svg") {
		t.Errorf("error = %v, want allowed-set in message", err)
	}
	if m.ImageRendersFailed.Load() != 1 {
		t.Errorf("ImageRendersFailed = %d, want 1", m.ImageRendersFailed.Load())
	}
}

func TestImageExecutor_InvalidDimensions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"width zero", map[string]any{"html": "<x/>", "width": 0}, "invalid width"},
		{"width too big", map[string]any{"html": "<x/>", "width": 10000}, "invalid width"},
		{"height too big", map[string]any{"html": "<x/>", "height": 10000}, "invalid height"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := NewImageExecutor(&fakeRenderer{}, NewMetrics())
			wf, step := newImageWorkflow(tc.cfg)
			err := ex.Execute(context.Background(), step, wf)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want contains %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "[64, 8192]") {
				t.Errorf("error = %v, want bounds in message", err)
			}
		})
	}
}

func TestImageExecutor_InvalidDensity(t *testing.T) {
	t.Parallel()
	ex := NewImageExecutor(&fakeRenderer{}, NewMetrics())
	wf, step := newImageWorkflow(map[string]any{"html": "<x/>", "density": 5})
	err := ex.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "density") {
		t.Fatalf("expected density error, got %v", err)
	}
}

func TestImageExecutor_RendererError(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	r := &fakeRenderer{err: errors.New("boom")}
	ex := NewImageExecutor(r, m)
	wf, step := newImageWorkflow(map[string]any{"html": "<x/>"})

	err := ex.Execute(context.Background(), step, wf)
	if err == nil {
		t.Fatal("expected error from renderer")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want wrapped 'boom'", err)
	}
	if m.ImageRendersFailed.Load() != 1 {
		t.Errorf("ImageRendersFailed = %d, want 1", m.ImageRendersFailed.Load())
	}
	if m.ImageRendersSuccess.Load() != 0 {
		t.Errorf("ImageRendersSuccess = %d, want 0", m.ImageRendersSuccess.Load())
	}
}

func TestImageExecutor_StepRefResolution(t *testing.T) {
	t.Parallel()
	r := &fakeRenderer{response: ImageRenderResult{Bytes: []byte("png"), MIMEType: "image/png"}}
	ex := NewImageExecutor(r, NewMetrics())

	step := &Step{ID: "render", Kind: StepImage, Config: map[string]any{
		"html": "$steps.prev.body",
	}}
	wf := NewWorkflow("wf-ref", "ref", "telegram:1", []Step{*step})
	wf.Context["prev"] = map[string]any{"body": "<div>resolved</div>"}

	if err := ex.Execute(context.Background(), &wf.Steps[0], wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.lastRequest.HTML != "<div>resolved</div>" {
		t.Errorf("html = %q, want resolved value", r.lastRequest.HTML)
	}
}

func TestImageExecutor_WorkspacePersistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &fakeRenderer{response: ImageRenderResult{
		Bytes:    []byte("PNGDATA"),
		MIMEType: "image/png",
	}}
	ex := NewImageExecutor(r, NewMetrics())
	ex.workspaceDir = dir

	wf, step := newImageWorkflow(map[string]any{"html": "<x/>"})
	if err := ex.Execute(context.Background(), step, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := step.Result.(map[string]any)
	path, _ := out["path"].(string)
	want := filepath.Join(dir, wf.ID, step.ID+".png")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "PNGDATA" {
		t.Errorf("file content = %q, want PNGDATA", data)
	}
}

func TestImageExecutor_MetricsErrorThenSuccess(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	r := &fakeRenderer{response: ImageRenderResult{Bytes: []byte("ok"), MIMEType: "image/png", SizeBytes: 2, DurationMS: 1}}
	ex := NewImageExecutor(r, m)

	wf1, step1 := newImageWorkflow(map[string]any{"html": ""}) // failure
	_ = ex.Execute(context.Background(), step1, wf1)

	wf2, step2 := newImageWorkflow(map[string]any{"html": "<x/>"}) // success
	if err := ex.Execute(context.Background(), step2, wf2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.ImageRendersFailed.Load() != 1 {
		t.Errorf("failed = %d, want 1", m.ImageRendersFailed.Load())
	}
	if m.ImageRendersSuccess.Load() != 1 {
		t.Errorf("success = %d, want 1", m.ImageRendersSuccess.Load())
	}
	if m.ImageBytesTotal.Load() != 2 {
		t.Errorf("bytes = %d, want 2", m.ImageBytesTotal.Load())
	}
}

func TestImageExecutor_NoRenderer(t *testing.T) {
	t.Parallel()
	ex := NewImageExecutor(nil, NewMetrics())
	wf, step := newImageWorkflow(map[string]any{"html": "<x/>"})
	err := ex.Execute(context.Background(), step, wf)
	if err == nil || !strings.Contains(err.Error(), "no ImageRenderer") {
		t.Fatalf("expected missing-renderer error, got %v", err)
	}
}

func TestImageExecutor_EngineWiring(t *testing.T) {
	t.Parallel()
	r := &fakeRenderer{response: ImageRenderResult{Bytes: []byte("x"), MIMEType: "image/png"}}
	store := newTestStore(t)
	dir := t.TempDir()
	engine := NewEngine(store,
		WithImageRenderer(r),
		WithImageWorkspace(dir),
	)
	ex, ok := engine.executors[StepImage].(*ImageExecutor)
	if !ok {
		t.Fatal("StepImage executor not registered")
	}
	if ex.workspaceDir != dir {
		t.Errorf("workspaceDir = %q, want %q", ex.workspaceDir, dir)
	}
	if ex.metrics == nil {
		t.Error("ImageExecutor metrics should be wired by NewEngine")
	}
}

func TestNormalizeStepKind_ImageAliases(t *testing.T) {
	t.Parallel()
	for _, alias := range []StepKind{"render_image", "image_render"} {
		if got := NormalizeStepKind(alias); got != StepImage {
			t.Errorf("NormalizeStepKind(%q) = %q, want %q", alias, got, StepImage)
		}
	}
	if !IsValidStepKind(StepImage) {
		t.Error("IsValidStepKind(StepImage) = false, want true")
	}
}
