package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestListScenarios_ProjectFilterNeverLeaksAnInvisibleProject pins the
// list's narrowing rule against the case a leak would hide in: filtering by
// a project the caller may NOT see contributes nothing -- an empty list,
// never the hidden project's scenarios -- while the same filter naming a
// visible project keeps working.
func TestListScenarios_ProjectFilterNeverLeaksAnInvisibleProject(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)

	mine := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	theirs := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"theirs"}, "owner": {"someone-else"}}))
	myScenario := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"mine"}, "project_id": {itoa(mine)}}))
	hiddenScenario := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"hidden"}, "project_id": {itoa(theirs)}}))

	// Unfiltered: only the visible project's scenario, the hidden one absent.
	rec := do(t, h, http.MethodGet, "/api/scenarios")
	if rec.Code != http.StatusOK {
		t.Fatalf("unfiltered list = %d (%s)", rec.Code, rec.Body.String())
	}
	got := decodeScenarioList(t, rec.Body.Bytes())
	if len(got) != 1 || got[0].ID != myScenario {
		t.Fatalf("unfiltered list = %+v, want only scenario %d -- never the invisible project's %d",
			got, myScenario, hiddenScenario)
	}

	// Filtering BY the invisible project: empty, not a leak.
	rec = do(t, h, http.MethodGet, "/api/scenarios?project_id="+itoa(theirs))
	if rec.Code != http.StatusOK {
		t.Fatalf("invisible-project filter = %d (%s)", rec.Code, rec.Body.String())
	}
	if got = decodeScenarioList(t, rec.Body.Bytes()); len(got) != 0 {
		t.Fatalf("invisible-project filter = %+v, want [] -- a filter can never widen the list", got)
	}

	// The same filter naming the visible project: unaffected.
	rec = do(t, h, http.MethodGet, "/api/scenarios?project_id="+itoa(mine))
	if rec.Code != http.StatusOK {
		t.Fatalf("visible-project filter = %d (%s)", rec.Code, rec.Body.String())
	}
	if got = decodeScenarioList(t, rec.Body.Bytes()); len(got) != 1 || got[0].ID != myScenario {
		t.Fatalf("visible-project filter = %+v, want scenario %d", got, myScenario)
	}
}

// jobWire is the whole CalibrationJob wire shape, so the alias-parity
// comparison below cannot silently skip a field one route set and the other
// did not.
type jobWire struct {
	ID               int64   `json:"id"`
	ExecutionID      int64   `json:"execution_id"`
	Phase            string  `json:"phase"`
	StepCount        int     `json:"step_count"`
	NextRequestedQPS float64 `json:"next_requested_qps"`
	Result           *struct {
		SaturatedBy string  `json:"saturated_by"`
		PerPodQPS   float64 `json:"per_pod_qps"`
	} `json:"result"`
	FailureReason string `json:"failure_reason"`
	CreatedTime   string `json:"created_time"`
	Steps         []struct {
		RequestedQPS   float64 `json:"requested_qps"`
		AchievedQPS    float64 `json:"achieved_qps"`
		Classification string  `json:"classification"`
	} `json:"steps"`
}

// TestTriggerScenarioCalibration_RecordsScenarioAndParityWithAlias covers
// the trigger contract end to end: the scenario-first route creates a job
// recording BOTH the execution and the scenario, and the deprecated
// execution-scoped alias -- pointed at the very execution the scenario
// route picked -- produces the same shape recording the same pair. Neither
// route may be distinguishable by response or by what lands in the store.
func TestTriggerScenarioCalibration_RecordsScenarioAndParityWithAlias(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, executionID, scenarioID := seedCalibration(t, h, store)
	ctx := context.Background()

	rec := do(t, h, http.MethodPost, "/api/scenarios/"+itoa(scenarioID)+"/calibration/trigger")
	if rec.Code != http.StatusCreated {
		t.Fatalf("scenario trigger = %d (%s)", rec.Code, rec.Body.String())
	}
	var viaScenario jobWire
	if err := json.Unmarshal(rec.Body.Bytes(), &viaScenario); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if viaScenario.ExecutionID != executionID || viaScenario.Phase != "pending" {
		t.Fatalf("scenario-trigger job = %+v, want a pending job for execution %d", viaScenario, executionID)
	}
	stored, err := store.GetCalibrationJob(ctx, viaScenario.ID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	if stored.ScenarioID != scenarioID || stored.ExecutionID != executionID {
		t.Fatalf("stored job = %+v, want execution_id=%d scenario_id=%d", stored, executionID, scenarioID)
	}

	// The deprecated alias, on the execution the scenario route picked.
	aliasRec := do(t, h, http.MethodPost, "/api/executions/"+itoa(executionID)+"/calibration/trigger")
	if aliasRec.Code != http.StatusCreated {
		t.Fatalf("alias trigger = %d (%s)", aliasRec.Code, aliasRec.Body.String())
	}
	var viaAlias jobWire
	if err := json.Unmarshal(aliasRec.Body.Bytes(), &viaAlias); err != nil {
		t.Fatalf("decode alias: %v (%s)", err, aliasRec.Body.String())
	}
	// Different jobs (fresh ids and timestamps), same everything else --
	// same execution, same phase, same shape. Field by field: the wire
	// structs hold slices, so there is no == to lean on.
	if viaAlias.ID == viaScenario.ID {
		t.Fatalf("alias produced the same job id %d, want a fresh job", viaAlias.ID)
	}
	if viaAlias.ExecutionID != viaScenario.ExecutionID ||
		viaAlias.Phase != viaScenario.Phase ||
		viaAlias.StepCount != viaScenario.StepCount ||
		viaAlias.NextRequestedQPS != viaScenario.NextRequestedQPS ||
		(viaAlias.Result == nil) != (viaScenario.Result == nil) ||
		viaAlias.FailureReason != viaScenario.FailureReason ||
		len(viaAlias.Steps) != len(viaScenario.Steps) {
		t.Fatalf("alias response differs: scenario route %+v vs alias %+v", viaScenario, viaAlias)
	}
	aliasStored, err := store.GetCalibrationJob(ctx, viaAlias.ID)
	if err != nil {
		t.Fatalf("GetCalibrationJob(alias): %v", err)
	}
	if aliasStored.ScenarioID != scenarioID {
		t.Fatalf("alias job ScenarioID = %d, want %d -- the alias records the scenario too",
			aliasStored.ScenarioID, scenarioID)
	}
}

// TestTriggerScenarioCalibration_ScenarioNothingCalibrates_400: naming a
// scenario with no CalibrateEngine execution is the caller's naming
// mistake -- a 400, never a silent pick of some other execution.
func TestTriggerScenarioCalibration_ScenarioNothingCalibrates_400(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	plainScenario := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"plain"}, "project_id": {itoa(projectID)}}))

	rec := do(t, h, http.MethodPost, "/api/scenarios/"+itoa(plainScenario)+"/calibration/trigger")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trigger (uncalibrated scenario) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// An unknown scenario is a 404 -- the same stance every per-scenario
	// route takes -- not a 400.
	rec = do(t, h, http.MethodPost, "/api/scenarios/424242/calibration/trigger")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("trigger (unknown scenario) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}
