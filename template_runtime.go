package workflow

import (
	"os"
	"slices"
)

// TemplateMeta is lightweight metadata for listing templates.
type TemplateMeta struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	Source      string            `json:"source"`
}

// TemplateRuntime loads workflow templates from a single directory.
type TemplateRuntime struct {
	dir   string
	store *TemplateStore
}

// NewTemplateRuntime creates a template runtime backed by a single directory.
func NewTemplateRuntime(dir string) *TemplateRuntime {
	var s *TemplateStore
	if dir != "" {
		s = NewTemplateStore(dir)
	}
	return &TemplateRuntime{dir: dir, store: s}
}

// Reload reloads templates from disk.
func (tr *TemplateRuntime) Reload() {
	if tr.store != nil {
		tr.store.Reload()
	}
}

// Dir returns the template directory path.
func (tr *TemplateRuntime) Dir() string {
	return tr.dir
}

// List returns all available templates sorted by name.
func (tr *TemplateRuntime) List() []TemplateMeta {
	if tr.store == nil {
		return nil
	}
	names := tr.store.List()
	out := make([]TemplateMeta, 0, len(names))
	for _, name := range names {
		if t, ok := tr.store.Get(name); ok {
			out = append(out, TemplateMeta{
				Name:        name,
				Description: t.Description,
				Params:      t.Params,
				Source:      "local",
			})
		}
	}
	slices.SortFunc(out, func(a, b TemplateMeta) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

// Instantiate creates a workflow from a template.
func (tr *TemplateRuntime) Instantiate(templateName, workflowID, owner string, params map[string]any) (*Workflow, error) {
	if tr.store != nil {
		if _, ok := tr.store.Get(templateName); ok {
			return tr.store.Instantiate(templateName, workflowID, owner, params)
		}
	}
	return nil, os.ErrNotExist
}
