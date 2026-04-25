package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ImageRenderer is the pluggable backend that turns HTML+geometry into image bytes.
// Implementations may include a satori-render HTTP adapter, an in-memory mock for
// tests, or a wkhtmltoimage shell wrapper. The engine never speaks to the renderer
// directly except through this interface.
type ImageRenderer interface {
	Render(ctx context.Context, req ImageRenderRequest) (ImageRenderResult, error)
}

// ImageRenderRequest captures everything an image step config can carry.
type ImageRenderRequest struct {
	HTML    string   // required
	Width   int      // pixels, default 1200
	Height  int      // pixels, default 630
	Format  string   // "png" | "jpeg" | "webp" | "svg"; default "png"
	Density int      // 1 | 2 | 3; default 1
	Fonts   []string // optional, names of pre-bundled or URL-loaded fonts
}

// ImageRenderResult is what the renderer returns.
type ImageRenderResult struct {
	Bytes      []byte
	MIMEType   string
	SizeBytes  int64
	DurationMS int64
	// Path is set by the executor when the image is persisted to disk;
	// useful so downstream steps can reference the file. Empty when caller
	// wants in-memory bytes only.
	Path string
}

// imageFormatExt maps a canonical format to its file extension.
var imageFormatExt = map[string]string{
	"png":  "png",
	"jpeg": "jpg",
	"webp": "webp",
	"svg":  "svg",
}

// imageFormatMIME maps a canonical format to its MIME type. Used as a fallback
// when the renderer does not populate ImageRenderResult.MIMEType.
var imageFormatMIME = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"webp": "image/webp",
	"svg":  "image/svg+xml",
}

const (
	imageDefaultWidth   = 1200
	imageDefaultHeight  = 630
	imageDefaultFormat  = "png"
	imageDefaultDensity = 1
	imageMinDimension   = 64
	imageMaxDimension   = 8192

	imageDirPerm  = 0o750
	imageFilePerm = 0o600
)

// ImageExecutor implements StepExecutor for StepImage. It validates step config,
// resolves HTML/font references against the workflow context, calls a pluggable
// ImageRenderer, optionally persists the bytes to disk, and exposes a result map
// downstream via wf.Context[stepID].
type ImageExecutor struct {
	renderer ImageRenderer
	metrics  *Metrics
	engine   *Engine // back-reference for cost recording (set by NewEngine)
	// workspaceDir, when set, makes the executor persist rendered bytes to a file
	// under workspaceDir/<workflow_id>/<step_id>.<ext> and populate Result.Path.
	// When empty, only in-memory bytes are returned via the result map.
	workspaceDir string
}

// NewImageExecutor builds an ImageExecutor wired to the given renderer + metrics.
// metrics may be nil; counters are guarded.
func NewImageExecutor(r ImageRenderer, metrics *Metrics) *ImageExecutor {
	return &ImageExecutor{renderer: r, metrics: metrics}
}

// Execute renders an image for the given step.
func (e *ImageExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	req, err := e.buildRequest(step, wf)
	if err != nil {
		e.recordFailure()
		return fmt.Errorf("image step %s: %w", step.ID, err)
	}

	if e.renderer == nil {
		e.recordFailure()
		return fmt.Errorf("image step %s: no ImageRenderer registered", step.ID)
	}

	start := time.Now()
	res, err := e.renderer.Render(ctx, req)
	if err != nil {
		e.recordFailure()
		return fmt.Errorf("image step %s: render: %w", step.ID, err)
	}

	if res.DurationMS == 0 {
		res.DurationMS = time.Since(start).Milliseconds()
	}
	if res.SizeBytes == 0 {
		res.SizeBytes = int64(len(res.Bytes))
	}
	if res.MIMEType == "" {
		res.MIMEType = imageFormatMIME[req.Format]
	}

	if e.workspaceDir != "" && res.Path == "" {
		path, perr := e.persist(wf.ID, step.ID, req.Format, res.Bytes)
		if perr != nil {
			e.recordFailure()
			return fmt.Errorf("image step %s: persist: %w", step.ID, perr)
		}
		res.Path = path
	}

	out := map[string]any{
		"path":        res.Path,
		"bytes_size":  res.SizeBytes,
		"mime_type":   res.MIMEType,
		"duration_ms": res.DurationMS,
		"format":      req.Format,
		"width":       req.Width,
		"height":      req.Height,
	}
	step.Result = out
	wf.Context[step.ID] = out

	e.recordSuccess(res.SizeBytes, res.DurationMS)

	if e.engine != nil {
		if costErr := e.engine.recordStepCost(wf, StepCost{
			StepID:     step.ID,
			Kind:       StepImage,
			Bytes:      res.SizeBytes,
			DurationMS: res.DurationMS,
		}); costErr != nil {
			return costErr
		}
	}
	return nil
}

// buildRequest reads step config, resolves $steps refs, applies defaults, and validates.
func (e *ImageExecutor) buildRequest(step *Step, wf *Workflow) (ImageRenderRequest, error) {
	cfg := step.Config

	htmlVal := resolveRef(cfg["html"], wf)
	html, _ := htmlVal.(string)
	if html == "" {
		return ImageRenderRequest{}, errors.New("missing html")
	}

	format, _ := cfg["format"].(string)
	if format == "" {
		format = imageDefaultFormat
	}
	if _, ok := imageFormatExt[format]; !ok {
		return ImageRenderRequest{}, fmt.Errorf("invalid format %q: must be one of png, jpeg, webp, svg", format)
	}

	width := intFromConfig(cfg["width"], imageDefaultWidth)
	height := intFromConfig(cfg["height"], imageDefaultHeight)
	if width < imageMinDimension || width > imageMaxDimension {
		return ImageRenderRequest{}, fmt.Errorf("invalid width %d: must be in [%d, %d]", width, imageMinDimension, imageMaxDimension)
	}
	if height < imageMinDimension || height > imageMaxDimension {
		return ImageRenderRequest{}, fmt.Errorf("invalid height %d: must be in [%d, %d]", height, imageMinDimension, imageMaxDimension)
	}

	density := intFromConfig(cfg["density"], imageDefaultDensity)
	if density < 1 || density > 3 {
		return ImageRenderRequest{}, fmt.Errorf("invalid density %d: must be 1, 2, or 3", density)
	}

	fonts := stringSliceFromConfig(cfg["fonts"])

	return ImageRenderRequest{
		HTML:    html,
		Width:   width,
		Height:  height,
		Format:  format,
		Density: density,
		Fonts:   fonts,
	}, nil
}

// persist writes rendered bytes to <workspaceDir>/<wfID>/<stepID>.<ext>.
func (e *ImageExecutor) persist(wfID, stepID, format string, data []byte) (string, error) {
	ext := imageFormatExt[format]
	dir := filepath.Join(e.workspaceDir, wfID)
	if err := os.MkdirAll(dir, imageDirPerm); err != nil {
		return "", err
	}
	path := filepath.Join(dir, stepID+"."+ext)
	if err := os.WriteFile(path, data, imageFilePerm); err != nil {
		return "", err
	}
	return path, nil
}

func (e *ImageExecutor) recordSuccess(bytes, durationMS int64) {
	if e.metrics == nil {
		return
	}
	e.metrics.ImageRendersSuccess.Add(1)
	e.metrics.ImageBytesTotal.Add(bytes)
	e.metrics.ImageDurationMSTotal.Add(durationMS)
}

func (e *ImageExecutor) recordFailure() {
	if e.metrics == nil {
		return
	}
	e.metrics.ImageRendersFailed.Add(1)
}

// intFromConfig coerces a config value (which may be int, int64, float64 from JSON)
// into an int, falling back to the default.
func intFromConfig(v any, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	return def
}

// stringSliceFromConfig coerces a []any or []string into []string. Returns nil when missing.
func stringSliceFromConfig(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
