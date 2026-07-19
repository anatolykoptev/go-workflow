package workflow

import "testing"

// TestResumableArtifact covers the go-wp #296 fix: a workflow cancelled at a
// pending approval gate (a BLOCK, not a terminal failure) is RESUMABLE via
// Reopen, and any surviving merged-render artifact path recorded on an earlier
// completed approval step is surfaced so an operator can salvage it rather than
// rebuild.
func TestResumableArtifact(t *testing.T) {
	tests := []struct {
		name         string
		wf           *Workflow
		wantResume   bool
		wantArtifact string
	}{
		{
			name: "cancelled at pending approval gate with /tmp artifact -> resumable + path",
			wf: &Workflow{
				State:       StateCancelled,
				CurrentStep: "go-live",
				Steps: []Step{
					{ID: "compose", Kind: StepApproval, State: StepCompleted},
					{ID: "go-live", Kind: StepApproval, State: StepPending},
				},
				Context: map[string]any{
					"compose": map[string]any{"render_file": "/tmp/go-wp/piter/merged-57827.jsonl"},
				},
			},
			wantResume:   true,
			wantArtifact: "/tmp/go-wp/piter/merged-57827.jsonl",
		},
		{
			name: "cancelled at pending gate, no recorded file -> resumable, empty path",
			wf: &Workflow{
				State:       StateCancelled,
				CurrentStep: "go-live",
				Steps: []Step{
					{ID: "go-live", Kind: StepApproval, State: StepPending},
				},
				Context: map[string]any{},
			},
			wantResume:   true,
			wantArtifact: "",
		},
		{
			name: "terminal failure (dead-lettered step, not a pending gate) -> NOT resumable",
			wf: &Workflow{
				State:       StateFailed,
				CurrentStep: "enrich",
				Steps: []Step{
					{ID: "enrich", Kind: StepTool, State: StepFailed},
				},
			},
			wantResume:   false,
			wantArtifact: "",
		},
		{
			name: "cancelled mid tool-step (current step is not a pending approval) -> NOT resumable",
			wf: &Workflow{
				State:       StateCancelled,
				CurrentStep: "enrich",
				Steps: []Step{
					{ID: "enrich", Kind: StepTool, State: StepRunning},
				},
			},
			wantResume:   false,
			wantArtifact: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotResume, gotArtifact := tt.wf.ResumableArtifact()
			if gotResume != tt.wantResume {
				t.Errorf("resumable = %v, want %v", gotResume, tt.wantResume)
			}
			if gotArtifact != tt.wantArtifact {
				t.Errorf("artifactPath = %q, want %q", gotArtifact, tt.wantArtifact)
			}
		})
	}
}

// TestBuildWFStatusOutput_SurfacesResumable ensures the wf_status output carries
// the resumable + artifact_path signal (additive fields, #296).
func TestBuildWFStatusOutput_SurfacesResumable(t *testing.T) {
	wf := &Workflow{
		State:       StateCancelled,
		CurrentStep: "go-live",
		Steps: []Step{
			{ID: "compose", Kind: StepApproval, State: StepCompleted},
			{ID: "go-live", Kind: StepApproval, State: StepPending},
		},
		Context: map[string]any{
			"compose": map[string]any{"render_file": "/tmp/go-wp/piter/merged-57827.jsonl"},
		},
	}
	out := BuildWFStatusOutput(wf)
	if !out.Resumable {
		t.Error("Resumable = false, want true for a cancel at a pending approval gate")
	}
	if out.ArtifactPath != "/tmp/go-wp/piter/merged-57827.jsonl" {
		t.Errorf("ArtifactPath = %q, want the surviving merged-render path", out.ArtifactPath)
	}
}

// TestResumableArtifact_ToolStepAndNestedAndDeterminism covers the PR #42
// review findings: the artifact path is usually recorded by a TOOL step (not
// the approval gate), may be nested one level, and a payload with multiple
// candidates must return a deterministic result.
func TestResumableArtifact_ToolStepAndNestedAndDeterminism(t *testing.T) {
	base := func(ctx map[string]any) *Workflow {
		return &Workflow{
			State:       StateCancelled,
			CurrentStep: "go-live",
			Steps: []Step{
				{ID: "merge", Kind: StepTool, State: StepCompleted},
				{ID: "go-live", Kind: StepApproval, State: StepPending},
			},
			Context: ctx,
		}
	}
	tests := []struct {
		name string
		ctx  map[string]any
		want string
	}{
		{
			name: "artifact recorded on a TOOL step (not approval) is found",
			ctx:  map[string]any{"merge": map[string]any{"render_file": "/tmp/go-wp/piter/merged-1.jsonl"}},
			want: "/tmp/go-wp/piter/merged-1.jsonl",
		},
		{
			name: "nested one level deep is found",
			ctx:  map[string]any{"merge": map[string]any{"result": map[string]any{"render_file": "/tmp/go-wp/piter/merged-2.jsonl"}}},
			want: "/tmp/go-wp/piter/merged-2.jsonl",
		},
		{
			name: "bare string payload is found",
			ctx:  map[string]any{"merge": "/tmp/go-wp/piter/merged-3.jsonl"},
			want: "/tmp/go-wp/piter/merged-3.jsonl",
		},
		{
			name: "multiple candidates -> smallest (deterministic), not random map order",
			ctx:  map[string]any{"merge": map[string]any{"a": "/tmp/z.jsonl", "b": "/tmp/a.jsonl"}},
			want: "/tmp/a.jsonl",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run repeatedly: a nondeterministic map scan would flake here.
			for i := 0; i < 20; i++ {
				resumable, path := base(tt.ctx).ResumableArtifact()
				if !resumable {
					t.Fatal("resumable = false, want true")
				}
				if path != tt.want {
					t.Fatalf("artifactPath = %q, want %q", path, tt.want)
				}
			}
		})
	}
}
