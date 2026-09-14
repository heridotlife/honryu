//go:build integration

package mysql_test

import (
	"context"
	"testing"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/test/dbtest"
)

// The seeds 0062/0063 lay down must produce a working template catalog, and
// every seeded template must instantiate into a scenario whose fragment the
// store path accepts -- a seed that fails here would 500 the very flow the
// templates exist for, so this is the guard that the SQL and the validator
// can never drift apart silently.
func TestMySQLSeededTemplates_InstantiateCleanly(t *testing.T) {
	db := dbtest.StartMySQL(t) // applies all migrations, seeds included
	repo := mysqladapter.NewRepository(db)
	svc := scenarioapp.NewService(repo, fake.NewObjectStore())
	ctx := context.Background()

	templates, err := repo.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	slugs := make([]string, 0, len(templates))
	for _, tpl := range templates {
		slugs = append(slugs, tpl.TemplateName)
	}
	want := []string{"httpbin-baseline", "httpbin-delay", "httpbin-spike"}
	if len(slugs) != len(want) {
		t.Fatalf("seeded templates = %v, want exactly %v", slugs, want)
	}
	for i, slug := range want {
		if slugs[i] != slug { // ListTemplates is id-ordered; the seeds land in order
			t.Fatalf("seeded templates = %v, want %v in seed order", slugs, want)
		}
	}

	proj, err := project.New("proj", "owner", "1")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	projectID, err := repo.CreateProject(ctx, proj)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for _, tpl := range templates {
		t.Run(tpl.TemplateName, func(t *testing.T) {
			sc, err := svc.Instantiate(ctx, tpl.ID, scenarioapp.InstantiateInput{
				Name: "clone-of-" + tpl.TemplateName, ProjectID: projectID,
				Overrides: scenarioapp.InstantiateOverrides{TargetURL: "https://clone.example"},
			})
			if err != nil {
				t.Fatalf("Instantiate: %v", err)
			}
			if sc.IsTemplate {
				t.Error("clone kept the template flag")
			}
			raw, err := svc.Requests(ctx, sc.ID)
			if err != nil {
				t.Fatalf("Requests(clone): %v", err)
			}
			if len(raw) == 0 {
				t.Fatal("clone stored an empty fragment")
			}
			// ValidateRequests runs the fragment through the same checks the
			// store path uses; a nil error means the clone is deployable.
			if _, err := svc.ValidateRequests(ctx, sc.ID, raw); err != nil {
				t.Errorf("cloned fragment does not validate: %v", err)
			}
		})
	}
}
