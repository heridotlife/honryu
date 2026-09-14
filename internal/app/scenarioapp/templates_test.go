// Phase 65: scenario templates. The flag's whole operator-visible contract
// lives in the service layer -- templates never appear in a project's list,
// the catalog lists only them, and instantiate clones one into an ordinary
// scenario through the exact create/store paths everything else uses.
package scenarioapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// seedTemplate writes a template straight into the store (as the seeder's
// SQL would) with a fragment, bypassing the service.
func seedTemplate(t *testing.T, store *fake.Store, name, slug, fragment string) int64 {
	t.Helper()
	tpl, err := scenario.NewTemplate(name, slug)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	id, err := store.CreateScenario(context.Background(), tpl)
	if err != nil {
		t.Fatalf("CreateScenario(template): %v", err)
	}
	if err := store.SetScenarioRequests(context.Background(), id, []byte(fragment)); err != nil {
		t.Fatalf("SetScenarioRequests(template): %v", err)
	}
	return id
}

const baselineFragment = "default-address: https://httpbin.org\nrequests:\n    - method: GET\n      url: /get\n"

func TestListByProject_ExcludesTemplates(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	seedTemplate(t, store, "HTTPbin baseline", "httpbin-baseline", baselineFragment)
	if _, err := svc.Create(ctx, "smoke", 10); err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := svc.ListByProject(ctx, 10)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(list) != 1 || list[0].Name != "smoke" {
		t.Fatalf("ListByProject = %+v, want only smoke (no templates)", list)
	}

	templates, err := svc.Templates(ctx)
	if err != nil {
		t.Fatalf("Templates: %v", err)
	}
	if len(templates) != 1 || templates[0].TemplateName != "httpbin-baseline" {
		t.Fatalf("Templates = %+v, want only httpbin-baseline", templates)
	}
}

func TestInstantiate_ClonesIntoAnOrdinaryScenario(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	tplID := seedTemplate(t, store, "HTTPbin baseline", "httpbin-baseline", baselineFragment)

	sc, err := svc.Instantiate(ctx, tplID, mustInput(t, "checkout-baseline", 10, ""))
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if sc.ID == 0 || sc.ID == tplID {
		t.Fatalf("Instantiate returned id %d, want a fresh non-template id", sc.ID)
	}
	if sc.IsTemplate || sc.TemplateName != "" {
		t.Errorf("clone = is_template=%v slug=%q, want an ordinary scenario", sc.IsTemplate, sc.TemplateName)
	}
	if sc.ProjectID != 10 {
		t.Errorf("clone ProjectID = %d, want 10", sc.ProjectID)
	}
	if sc.Kind != scenario.KindPortable {
		t.Errorf("clone kind = %q, want portable", sc.Kind)
	}

	// The fragment is cloned byte-for-byte when nothing is overridden.
	got, err := svc.Requests(ctx, sc.ID)
	if err != nil {
		t.Fatalf("Requests(clone): %v", err)
	}
	if string(got) != baselineFragment {
		t.Errorf("clone fragment = %q, want the template's verbatim %q", got, baselineFragment)
	}

	// The template itself is untouched and still catalogued.
	tpl, err := svc.Get(ctx, tplID)
	if err != nil {
		t.Fatalf("Get(template): %v", err)
	}
	if !tpl.IsTemplate {
		t.Error("instantiate mutated the template's flag")
	}
	if templates, _ := svc.Templates(ctx); len(templates) != 1 {
		t.Errorf("Templates after instantiate = %d entries, want 1", len(templates))
	}
}

// mustInput packs the handler-shaped fields (name, project, optional
// target_url override) into one struct.
func mustInput(t *testing.T, name string, projectID int64, targetURL string) scenarioapp.InstantiateInput {
	t.Helper()
	in := scenarioapp.InstantiateInput{Name: name, ProjectID: projectID}
	if targetURL != "" {
		in.Overrides.TargetURL = targetURL
	}
	return in
}

func TestInstantiate_AppliesTargetURLOverride(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	tplID := seedTemplate(t, store, "HTTPbin baseline", "httpbin-baseline", baselineFragment)

	sc, err := svc.Instantiate(ctx, tplID, mustInput(t, "staging-baseline", 10, " http://staging.example/ "))
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	raw, err := svc.Requests(ctx, sc.ID)
	if err != nil {
		t.Fatalf("Requests(clone): %v", err)
	}
	// Normalized exactly like the NewTest flow's own target: trimmed, one
	// trailing slash dropped. The template's fragment is untouched.
	if !strings.Contains(string(raw), "default-address: http://staging.example") {
		t.Errorf("clone fragment = %q, want the overridden default-address", raw)
	}
	if !strings.Contains(string(raw), "url: /get") {
		t.Errorf("clone fragment = %q, want the template's requests preserved", raw)
	}
	tplRaw, err := svc.Requests(ctx, tplID)
	if err != nil {
		t.Fatalf("Requests(template): %v", err)
	}
	if string(tplRaw) != baselineFragment {
		t.Errorf("template fragment changed: %q", tplRaw)
	}
}

func TestInstantiate_RefusesNonTemplatesAndUnknown(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()

	ordinary, err := svc.Create(ctx, "smoke", 10)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Instantiate(ctx, ordinary.ID, mustInput(t, "copy", 10, "")); !errors.Is(err, scenarioapp.ErrScenarioNotTemplate) {
		t.Fatalf("Instantiate(ordinary scenario) = %v, want ErrScenarioNotTemplate", err)
	}
	if _, err := svc.Instantiate(ctx, 999999, mustInput(t, "copy", 10, "")); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Instantiate(unknown) = %v, want ErrNotFound", err)
	}
}

func TestInstantiate_ValidatesNameThroughTheCreatePath(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	tplID := seedTemplate(t, store, "HTTPbin baseline", "httpbin-baseline", baselineFragment)

	if _, err := svc.Instantiate(ctx, tplID, mustInput(t, "  ", 10, "")); !errors.Is(err, scenario.ErrNameRequired) {
		t.Fatalf("Instantiate(blank name) = %v, want ErrNameRequired", err)
	}
	// The refusal happened before anything was created.
	if list, _ := svc.ListByProject(ctx, 10); len(list) != 0 {
		t.Errorf("a rejected instantiate left %d scenarios behind", len(list))
	}
}

// A template whose stored fragment would not validate (writable only by
// bypassing SetRequests, as a hand-edited database could) must fail to
// instantiate AND leave no half-created scenario behind.
func TestInstantiate_RollsBackWhenTheFragmentIsRefused(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	tpl, err := scenario.NewTemplate("broken", "broken-template")
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	tplID, err := store.CreateScenario(ctx, tpl)
	if err != nil {
		t.Fatalf("CreateScenario(template): %v", err)
	}
	// Garbage straight into the store: no requests at all, which SetRequests
	// (and only SetRequests -- the one validate-and-store path) refuses.
	if err := store.SetScenarioRequests(ctx, tplID, []byte("default-address: https://httpbin.org\n")); err != nil {
		t.Fatalf("seed fragment: %v", err)
	}

	if _, err := svc.Instantiate(ctx, tplID, mustInput(t, "from-broken", 10, "")); !errors.Is(err, scenarioapp.ErrRequestsInvalid) {
		t.Fatalf("Instantiate(broken template) = %v, want ErrRequestsInvalid", err)
	}
	if list, _ := svc.ListByProject(ctx, 10); len(list) != 0 {
		t.Errorf("a failed instantiate left %d scenarios behind, want rollback", len(list))
	}
}

func TestInstantiate_StampsTenantFromProject(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	tenantID := int64(55)
	p, err := project.New("proj", "owner", "123")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	p.TenantID = &tenantID
	projectID, err := store.CreateProject(ctx, p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	tplID := seedTemplate(t, store, "HTTPbin baseline", "httpbin-baseline", baselineFragment)
	sc, err := svc.Instantiate(ctx, tplID, mustInput(t, "tenanted", projectID, ""))
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if sc.TenantID == nil || *sc.TenantID != tenantID {
		t.Errorf("clone TenantID = %v, want %d (the project's)", sc.TenantID, tenantID)
	}
}
