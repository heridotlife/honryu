package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// scenarioListRow is the slice of the scenario wire shape the list tests
// pin: identity, the phase 67a kind field, and the phase 70 last_run
// summary (nil pointer = the null the wire promises when there is no
// verdict yet).
type scenarioListRow struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ProjectID   int64  `json:"project_id"`
	Kind        string `json:"kind"`
	IsTemplate  bool   `json:"is_template"`
	TemplateNam string `json:"template_name"`
	LastRun     *struct {
		ExecutionID int64  `json:"execution_id"`
		Outcome     string `json:"outcome"`
		StartedAt   string `json:"started_at"`
	} `json:"last_run"`
}

func decodeScenarioList(t *testing.T, body []byte) []scenarioListRow {
	t.Helper()
	var out []scenarioListRow
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode scenario list: %v (%s)", err, string(body))
	}
	return out
}

func TestListScenarios_FlatListAcrossProjectsExcludingTemplates(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	ctx := context.Background()

	projA := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	projB := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"api"}, "owner": {"honryu"}}))
	scenarioA := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"alpha"}, "project_id": {itoa(projA)}}))
	scenarioB := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"beta"}, "project_id": {itoa(projB)}}))

	// A template is a scenario with the flag; seed it below the service so
	// the test controls exactly what the list must exclude. Templates are
	// global (no project), so nothing about them belongs in a caller's
	// scenario list.
	tpl, err := scenario.NewTemplate("Baseline", "httpbin-baseline")
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	if _, err := store.CreateScenario(ctx, tpl); err != nil {
		t.Fatalf("CreateScenario(template): %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios")
	if rec.Code != http.StatusOK {
		t.Fatalf("list scenarios = %d (%s)", rec.Code, rec.Body.String())
	}
	got := decodeScenarioList(t, rec.Body.Bytes())
	if len(got) != 2 {
		t.Fatalf("list = %+v, want exactly the two runnable scenarios", got)
	}
	if got[0].ID != scenarioA || got[1].ID != scenarioB {
		t.Fatalf("list ids = %d,%d, want %d,%d (id order)", got[0].ID, got[1].ID, scenarioA, scenarioB)
	}
	for _, row := range got {
		if row.IsTemplate || row.TemplateNam != "" {
			t.Fatalf("list row %+v carries template data", row)
		}
		if row.Kind != "portable" {
			t.Fatalf("list row kind = %q, want portable", row.Kind)
		}
	}
	if got[0].ProjectID != projA || got[1].ProjectID != projB {
		t.Fatalf("list rows = %+v, want one per project", got)
	}
}

func TestListScenarios_ProjectFilterNarrowsWithoutWidening(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)

	projA := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	projB := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"api"}, "owner": {"honryu"}}))
	scenarioA := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"alpha"}, "project_id": {itoa(projA)}}))
	_ = decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"beta"}, "project_id": {itoa(projB)}}))

	rec := do(t, h, http.MethodGet, "/api/scenarios?project_id="+itoa(projA))
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list = %d (%s)", rec.Code, rec.Body.String())
	}
	got := decodeScenarioList(t, rec.Body.Bytes())
	if len(got) != 1 || got[0].ID != scenarioA || got[0].ProjectID != projA {
		t.Fatalf("filtered list = %+v, want only project %d's scenario", got, projA)
	}

	// A filter naming a project with no scenarios is an empty list, not an
	// error -- and never some other project's rows.
	rec = do(t, h, http.MethodGet, "/api/scenarios?project_id=424242")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty filter = %d (%s)", rec.Code, rec.Body.String())
	}
	if got = decodeScenarioList(t, rec.Body.Bytes()); len(got) != 0 {
		t.Fatalf("empty filter = %+v, want []", got)
	}

	// A malformed filter is the caller's input mistake.
	rec = do(t, h, http.MethodGet, "/api/scenarios?project_id=abc")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter = %d (%s)", rec.Code, rec.Body.String())
	}
}

// The last-run column must reproduce the scenario detail page's per-row
// probe for the whole list at once: the newest execution bound to the
// scenario, and THAT execution's newest report -- never a report from an
// older execution (however new it is), never the newest execution's older
// report, and null when there is no verdict yet.
func TestListScenarios_LastRunFromNewestExecutionNewestReport(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	ctx := context.Background()

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	withRuns := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"with-runs"}, "project_id": {itoa(projectID)}}))
	pending := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"pending"}, "project_id": {itoa(projectID)}}))
	idle := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"idle"}, "project_id": {itoa(projectID)}}))

	// Two executions bound to with-runs, created in order: the second is
	// the newest either way -- created_time order when the clock separates
	// them, the id tiebreak when the column's precision does not.
	older := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"older"}, "project_id": {itoa(projectID)}}))
	newest := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"newest"}, "project_id": {itoa(projectID)}}))
	for _, bind := range []struct{ exec, scenario int64 }{
		{older, withRuns}, {newest, withRuns},
	} {
		if err := store.StoreLoadProfile(ctx, bind.exec, false, []loadprofile.Entry{
			{Name: "bound", ScenarioID: bind.scenario, Engines: 1, Concurrency: 5, Duration: 30},
		}); err != nil {
			t.Fatalf("StoreLoadProfile(%d): %v", bind.exec, err)
		}
	}

	saveReport := func(rep report.Report) {
		t.Helper()
		if rep.EndedAt.IsZero() {
			rep.EndedAt = rep.StartedAt.Add(time.Minute)
		}
		if err := store.SaveReport(ctx, rep); err != nil {
			t.Fatalf("SaveReport(run %d): %v", rep.RunID, err)
		}
	}
	// The trap: the OLDER execution's report starts last of all. A column
	// that surfaced it picked a report across executions instead of the
	// newest execution's own.
	saveReport(report.Report{ExecutionID: older, ScenarioID: withRuns, RunID: 901,
		Outcome: taurus.OutcomeError, StartedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)})
	// The newest execution's two reports, in order: the later one wins.
	saveReport(report.Report{ExecutionID: newest, ScenarioID: withRuns, RunID: 902,
		Outcome: taurus.OutcomePassed, StartedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)})
	saveReport(report.Report{ExecutionID: newest, ScenarioID: withRuns, RunID: 903,
		Outcome: taurus.OutcomeFailed, StartedAt: time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)})

	// Bound, but no report yet: null, not a verdict.
	pendingExec := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"pending-run"}, "project_id": {itoa(projectID)}}))
	if err := store.StoreLoadProfile(ctx, pendingExec, false, []loadprofile.Entry{
		{Name: "pending", ScenarioID: pending, Engines: 1, Concurrency: 5, Duration: 30},
	}); err != nil {
		t.Fatalf("StoreLoadProfile(%d): %v", pendingExec, err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios")
	if rec.Code != http.StatusOK {
		t.Fatalf("list scenarios = %d (%s)", rec.Code, rec.Body.String())
	}
	got := decodeScenarioList(t, rec.Body.Bytes())
	byID := make(map[int64]scenarioListRow, len(got))
	for _, row := range got {
		byID[row.ID] = row
	}

	if lr := byID[withRuns].LastRun; lr == nil {
		t.Fatalf("with-runs row carries no last_run, want the newest execution's newest report")
	} else if lr.ExecutionID != newest || lr.Outcome != "failed" {
		t.Fatalf("with-runs last_run = {exec %d, %s}, want {exec %d, failed}", lr.ExecutionID, lr.Outcome, newest)
	} else if started, err := time.Parse(time.RFC3339, lr.StartedAt); err != nil || !started.Equal(time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("with-runs last_run.started_at = %q (%v), want the newest report's start", lr.StartedAt, err)
	}
	if lr := byID[pending].LastRun; lr != nil {
		t.Fatalf("pending row last_run = %+v, want null (no report yet)", lr)
	}
	if lr := byID[idle].LastRun; lr != nil {
		t.Fatalf("idle row last_run = %+v, want null (never bound)", lr)
	}

	// The key is present even when null -- "no verdict yet", never a
	// silently missing field.
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw list: %v", err)
	}
	for _, row := range raw {
		if _, ok := row["last_run"]; !ok {
			t.Fatalf("list row %s carries no last_run key, want an explicit null", string(row["id"]))
		}
	}

	// The single-scenario response carries no last_run key at all: the
	// verdict column belongs to the list, and a null there would claim
	// "never ran" about a scenario the endpoint never looked up.
	rec = do(t, h, http.MethodGet, "/api/scenarios/"+itoa(withRuns))
	if rec.Code != http.StatusOK {
		t.Fatalf("get scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	var single map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &single); err != nil {
		t.Fatalf("decode single scenario: %v", err)
	}
	if _, ok := single["last_run"]; ok {
		t.Fatal("single scenario response carries last_run; the key belongs to the list only")
	}
}

func TestListScenarioExecutions_NewestFirstAcrossRigs(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	ctx := context.Background()

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	otherScenario := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"other"}, "project_id": {itoa(projectID)}}))
	lonelyScenario := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"lonely"}, "project_id": {itoa(projectID)}}))

	// Three executions: two bound to the scenario (the newest created last),
	// one bound only to another scenario and one bound to nothing -- none of
	// those may leak into the list.
	first := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"first"}, "project_id": {itoa(projectID)}}))
	second := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"second"}, "project_id": {itoa(projectID)}}))
	unbound := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"elsewhere"}, "project_id": {itoa(projectID)}}))
	if err := store.StoreLoadProfile(ctx, first, false, []loadprofile.Entry{
		{Name: "target", ScenarioID: scenarioID, Engines: 1, Concurrency: 5, Duration: 30},
	}); err != nil {
		t.Fatalf("StoreLoadProfile(first): %v", err)
	}
	if err := store.StoreLoadProfile(ctx, second, false, []loadprofile.Entry{
		{Name: "target", ScenarioID: scenarioID, Engines: 2, Concurrency: 5, Duration: 30},
	}); err != nil {
		t.Fatalf("StoreLoadProfile(second): %v", err)
	}
	if err := store.StoreLoadProfile(ctx, unbound, false, []loadprofile.Entry{
		{Name: "other", ScenarioID: otherScenario, Engines: 1, Concurrency: 5, Duration: 30},
	}); err != nil {
		t.Fatalf("StoreLoadProfile(unbound): %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/executions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list executions = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		ProjectID int64  `json:"project_id"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(got) != 2 || got[0].ID != second || got[1].ID != first {
		t.Fatalf("list = %+v, want [second first] newest-first", got)
	}
	if got[0].ProjectID != projectID || got[0].Kind != "normal" {
		t.Fatalf("list row = %+v, want the executionSummary shape", got[0])
	}

	// A scenario nothing runs: an empty list, never an error.
	rec = do(t, h, http.MethodGet, "/api/scenarios/"+itoa(lonelyScenario)+"/executions")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty list = %d (%s)", rec.Code, rec.Body.String())
	}

	// An unknown scenario is a 404, not an empty list -- the same stance the
	// per-scenario reads take.
	rec = do(t, h, http.MethodGet, "/api/scenarios/424242/executions")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown scenario = %d (%s)", rec.Code, rec.Body.String())
	}
}
