// Package workflowtemplates is the built-in catalog of starter workflow
// documents (ADR-024). Templates are embedded at build time, validated
// with the same static pipeline as user documents, and never read by
// the engine: using one copies its document into a new draft version.
package workflowtemplates

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/validate"
)

//go:embed catalog/*.yaml
var catalogFS embed.FS

var (
	idRe     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	tagKeyRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_./-]{0,61}[A-Za-z0-9])?$`)
	tagValRe = regexp.MustCompile(`^[A-Za-z0-9_.:/@+ -]{0,128}$`)
)

// Template is one catalog entry. Every template is a workflow template;
// Tags are free-form key/value metadata, not a type hierarchy.
type Template struct {
	ID          string
	Title       string
	Summary     string
	Description string
	Tags        map[string]string
	Order       int
	Spec        workflowspec.Workflow
	YAML        string // the workflow document, key order and comments kept
}

type file struct {
	ID          string            `yaml:"id"`
	Title       string            `yaml:"title"`
	Summary     string            `yaml:"summary"`
	Description string            `yaml:"description"`
	Tags        map[string]string `yaml:"tags"`
	Order       int               `yaml:"order"`
	Workflow    yaml.Node         `yaml:"workflow"`
}

var (
	loadOnce sync.Once
	loaded   []Template
	loadErr  error
)

// All returns the catalog ordered by Order then ID. The catalog is
// parsed once; an invalid embedded template is a build defect and is
// reported by every call (and fails the package tests).
func All() ([]Template, error) {
	loadOnce.Do(func() { loaded, loadErr = load(catalogFS) })
	return loaded, loadErr
}

// Get returns one template by id.
func Get(id string) (Template, bool, error) {
	all, err := All()
	if err != nil {
		return Template{}, false, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, true, nil
		}
	}
	return Template{}, false, nil
}

func load(fsys fs.FS) ([]Template, error) {
	names, err := fs.Glob(fsys, "catalog/*.yaml")
	if err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		t, err := parse(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("workflowtemplates: %s: %w", name, err)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("workflowtemplates: duplicate id %q", t.ID)
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func parse(fsys fs.FS, name string) (Template, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Template{}, err
	}
	var f file
	if err := yaml.Unmarshal(body, &f); err != nil {
		return Template{}, err
	}
	if !idRe.MatchString(f.ID) || f.ID+".yaml" != path.Base(name) {
		return Template{}, fmt.Errorf("id %q must be a DNS label matching the file name", f.ID)
	}
	if f.Title == "" || f.Summary == "" || f.Workflow.Kind == 0 {
		return Template{}, fmt.Errorf("title, summary and workflow are required")
	}
	if f.Tags == nil {
		f.Tags = map[string]string{}
	}
	for k, v := range f.Tags {
		if !tagKeyRe.MatchString(k) || !tagValRe.MatchString(v) {
			return Template{}, fmt.Errorf("tag %q=%q is not a valid key/value pair", k, v)
		}
	}
	var doc any
	if err := f.Workflow.Decode(&doc); err != nil {
		return Template{}, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return Template{}, err
	}
	spec, err := workflowspec.Decode(raw, "application/json")
	if err != nil {
		return Template{}, err
	}
	if errs := validate.Static(spec); len(errs) > 0 {
		return Template{}, fmt.Errorf("%s: %s", errs[0].Path, errs[0].Message)
	}
	docYAML, err := yaml.Marshal(&f.Workflow)
	if err != nil {
		return Template{}, err
	}
	return Template{ID: f.ID, Title: f.Title, Summary: f.Summary,
		Description: f.Description, Tags: f.Tags, Order: f.Order,
		Spec: spec, YAML: string(docYAML)}, nil
}
