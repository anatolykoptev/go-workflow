package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateRuntimeListAndInstantiate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	tmpl := `{"name":"Test","description":"a test template","steps":[{"id":"s","kind":"message","config":{"content":"hello"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "test.json"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewTemplateRuntime(dir)
	list := rt.List()
	if len(list) != 1 {
		t.Fatalf("templates=%d want 1", len(list))
	}
	if list[0].Name != "test" {
		t.Fatalf("name=%s want test", list[0].Name)
	}
	if list[0].Source != "local" {
		t.Fatalf("source=%s want local", list[0].Source)
	}

	wf, err := rt.Instantiate("test", "wf1", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name != "Test" {
		t.Fatalf("name=%s want Test", wf.Name)
	}
}

func TestTemplateRuntimeEmptyDir(t *testing.T) {
	rt := NewTemplateRuntime("")
	if list := rt.List(); list != nil {
		t.Fatalf("expected nil, got %v", list)
	}
	if _, err := rt.Instantiate("x", "wf1", "owner", nil); err == nil {
		t.Fatal("expected error for empty runtime")
	}
}
