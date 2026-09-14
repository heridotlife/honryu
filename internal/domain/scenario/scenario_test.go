package scenario_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/scenario"
)

func TestNew_Valid(t *testing.T) {
	t.Parallel()

	p, err := scenario.New("  smoke-test  ", 7)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if p.Name != "smoke-test" {
		t.Errorf("Name = %q, want trimmed smoke-test", p.Name)
	}
	if p.ProjectID != 7 {
		t.Errorf("ProjectID = %d, want 7", p.ProjectID)
	}
	if p.ID != 0 {
		t.Errorf("ID = %d, want 0 (assigned by repository)", p.ID)
	}
}

func TestNew_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		planName  string
		projectID int64
		wantErr   error
	}{
		{"empty name", "", 1, scenario.ErrNameRequired},
		{"blank name", "   ", 1, scenario.ErrNameRequired},
		{"name too long", strings.Repeat("a", 101), 1, scenario.ErrNameTooLong},
		{"zero project", "smoke", 0, scenario.ErrProjectRequired},
		{"negative project", "smoke", -1, scenario.ErrProjectRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := scenario.New(tc.planName, tc.projectID)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("New(%q,%d) err = %v, want %v", tc.planName, tc.projectID, err, tc.wantErr)
			}
		})
	}
}

func TestNewTemplate_Valid(t *testing.T) {
	t.Parallel()

	tpl, err := scenario.NewTemplate("  HTTPbin baseline  ", "  httpbin-baseline  ")
	if err != nil {
		t.Fatalf("NewTemplate: unexpected error: %v", err)
	}
	if tpl.Name != "HTTPbin baseline" || tpl.TemplateName != "httpbin-baseline" {
		t.Errorf("names = %q/%q, want trimmed display/slug", tpl.Name, tpl.TemplateName)
	}
	if !tpl.IsTemplate || tpl.Kind != scenario.KindPortable || tpl.Engine != "" {
		t.Errorf("template = %+v, want IsTemplate portable with no engine", tpl)
	}
	if tpl.ProjectID != 0 {
		t.Errorf("ProjectID = %d, want 0 (a template is global)", tpl.ProjectID)
	}
	if err := tpl.Validate(); err != nil {
		t.Errorf("template does not validate: %v", err)
	}
}

func TestNewTemplate_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		displayName  string
		templateName string
		wantErr      error
	}{
		{"missing slug", "baseline", "", scenario.ErrTemplateNameRequired},
		{"blank slug", "baseline", "   ", scenario.ErrTemplateNameRequired},
		{"slug too long", "baseline", strings.Repeat("a", 129), scenario.ErrTemplateNameTooLong},
		{"missing display name", "", "httpbin-baseline", scenario.ErrNameRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := scenario.NewTemplate(tc.displayName, tc.templateName); !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewTemplate = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// The template slug is only valid on a template: a plain scenario carrying
// one is as wrong as a template missing one, and must refuse to validate.
func TestValidate_TemplateNameOnlyOnTemplates(t *testing.T) {
	t.Parallel()

	s, err := scenario.New("smoke", 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.TemplateName = "httpbin-baseline"
	if err := s.Validate(); !errors.Is(err, scenario.ErrTemplateNameRequired) {
		t.Fatalf("non-template with slug = %v, want ErrTemplateNameRequired", err)
	}
}
