package workflow

import (
	"reflect"
	"testing"
)

func TestSplitPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"simple", "a", []string{"a"}},
		{"dotted", "a.b.c", []string{"a", "b", "c"}},
		{"array_index", "items[1]", []string{"items", "[1]"}},
		{"array_nested", "a.b[0].c", []string{"a", "b", "[0]", "c"}},
		{"wildcard", "users[*].name", []string{"users", "[*]", "name"}},
		{"complex", "a.b[0].c[*].d", []string{"a", "b", "[0]", "c", "[*]", "d"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := splitPath(tt.path)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolvePath_Simple(t *testing.T) {
	t.Parallel()
	root := map[string]any{"name": "alice"}
	got := resolvePath(root, "name")
	if got != "alice" {
		t.Errorf("got %v, want %q", got, "alice")
	}
}

func TestResolvePath_Nested(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"data": map[string]any{
			"items": map[string]any{
				"count": 42,
			},
		},
	}
	got := resolvePath(root, "data.items.count")
	if got != 42 {
		t.Errorf("got %v, want 42", got)
	}
}

func TestResolvePath_ArrayIndex(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"items": []any{"a", "b", "c"},
	}
	got := resolvePath(root, "items[1]")
	if got != "b" {
		t.Errorf("got %v, want %q", got, "b")
	}
}

func TestResolvePath_ArrayNested(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"users": []any{
			map[string]any{"name": "alice"},
			map[string]any{"name": "bob"},
		},
	}
	got := resolvePath(root, "users[0].name")
	if got != "alice" {
		t.Errorf("got %v, want %q", got, "alice")
	}
}

func TestResolvePath_Wildcard(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"users": []any{
			map[string]any{"name": "alice", "age": 30},
			map[string]any{"name": "bob", "age": 25},
		},
	}
	got := resolvePath(root, "users[*].name")
	want := []any{"alice", "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolvePath_Missing(t *testing.T) {
	t.Parallel()
	root := map[string]any{"a": 1}
	got := resolvePath(root, "b")
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestResolvePath_OutOfBounds(t *testing.T) {
	t.Parallel()
	root := map[string]any{
		"items": []any{"a", "b"},
	}
	got := resolvePath(root, "items[5]")
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestResolveRef_NestedPath(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID: "wf1",
		Context: map[string]any{
			"fetch": map[string]any{
				"data": map[string]any{
					"name": "go-workflow",
				},
			},
		},
	}

	got := resolveRef("$steps.fetch.data.name", wf)
	if got != "go-workflow" {
		t.Errorf("got %v, want %q", got, "go-workflow")
	}
}

func TestResolveRef_DollarRef(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID: "wf1",
		Context: map[string]any{
			"s1": map[string]any{
				"items": []any{
					map[string]any{"id": "first"},
					map[string]any{"id": "second"},
				},
			},
		},
	}

	val := map[string]any{"$ref": "s1.items[0].id"}
	got := resolveRefValue(val, wf)
	if got != "first" {
		t.Errorf("got %v, want %q", got, "first")
	}
}

func TestResolveRefValue_NoRef(t *testing.T) {
	t.Parallel()
	wf := &Workflow{
		ID:      "wf1",
		Context: map[string]any{"s1": "hello"},
	}

	// Non-map value should fall through to resolveRef.
	got := resolveRefValue("plain", wf)
	if got != "plain" {
		t.Errorf("got %v, want %q", got, "plain")
	}

	// Map without $ref should be returned as-is.
	m := map[string]any{"key": "value"}
	got = resolveRefValue(m, wf)
	gotMap, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if gotMap["key"] != "value" {
		t.Errorf("got %v, want map with key=value", got)
	}
}
