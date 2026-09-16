package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/threshold"
)

// thresholdMaxBodyBytes bounds a replace-all PUT body: a threshold list a
// human edits is a handful of rows; 16 KiB is orders of magnitude past that
// and still a round number of pages.
const thresholdMaxBodyBytes = 1 << 14

// thresholdResponse is one row of the scenario-thresholds wire: the stored
// definition with its id and stamp (the editor syncs on them), the metric
// and comparison as the enum's wire spellings.
type thresholdResponse struct {
	ID          int64     `json:"id"`
	ScenarioID  int64     `json:"scenario_id"`
	Metric      string    `json:"metric"`
	Comparison  string    `json:"comparison"`
	Value       float64   `json:"value"`
	CreatedTime time.Time `json:"created_time"`
}

func toThresholdResponse(t threshold.Threshold) thresholdResponse {
	return thresholdResponse{
		ID:          t.ID,
		ScenarioID:  t.ScenarioID,
		Metric:      string(t.Metric),
		Comparison:  string(t.Comparison),
		Value:       t.Value,
		CreatedTime: t.CreatedTime,
	}
}

func toThresholdResponses(list []threshold.Threshold) []thresholdResponse {
	out := make([]thresholdResponse, 0, len(list))
	for _, t := range list {
		out = append(out, toThresholdResponse(t))
	}
	return out
}

// thresholdsGate rejects the request unless the threshold service is wired,
// before any authorization: a service-less router answers 404 "not
// configured" -- the same contract every optional service follows.
func (h *handlers) thresholdsGate(w http.ResponseWriter) bool {
	if h.deps.Thresholds == nil {
		writeError(w, http.StatusNotFound, "thresholds not configured")
		return false
	}
	return true
}

// listScenarioThresholds serves GET /api/scenarios/{scenario_id}/thresholds:
// the scenario's thresholds, definition order, always an array -- empty when
// none are defined, never null.
func (h *handlers) listScenarioThresholds(w http.ResponseWriter, r *http.Request) {
	if !h.thresholdsGate(w) {
		return
	}
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	list, err := h.deps.Thresholds.List(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toThresholdResponses(list))
}

// replaceThresholdsBody is the PUT's replace-all body: the whole list is the
// truth, exactly as the editor-save semantics want. An empty list (or an
// absent one, decoded the same) clears the scenario's thresholds.
type replaceThresholdsBody struct {
	Thresholds []struct {
		Metric     string  `json:"metric"`
		Comparison string  `json:"comparison"`
		Value      float64 `json:"value"`
	} `json:"thresholds"`
}

// replaceScenarioThresholds serves PUT /api/scenarios/{scenario_id}/thresholds:
// replace-all, idempotent by construction. The response is the stored set
// (ids included) so the editor can sync without a second fetch.
func (h *handlers) replaceScenarioThresholds(w http.ResponseWriter, r *http.Request) {
	if !h.thresholdsGate(w) {
		return
	}
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	var body replaceThresholdsBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, thresholdMaxBodyBytes)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "body must be JSON: {\"thresholds\": [{metric, comparison, value}]}")
		return
	}
	// An absent body decodes to EOF -- that is the empty set, not a
	// malformed request: clearing via PUT is the replace-all contract.
	defs := make([]threshold.Threshold, 0, len(body.Thresholds))
	for _, row := range body.Thresholds {
		defs = append(defs, threshold.Threshold{
			Metric:     threshold.Metric(row.Metric),
			Comparison: threshold.Comparison(row.Comparison),
			Value:      row.Value,
		})
	}
	stored, err := h.deps.Thresholds.Replace(r.Context(), id, defs)
	if err != nil {
		// A rejected definition is a domain validation error (unknown
		// metric, out-of-range value): the caller's mistake, told in the
		// domain's message. Unknown scenario keeps the usual 404 mapping.
		switch {
		case errors.Is(err, threshold.ErrMetricUnknown),
			errors.Is(err, threshold.ErrComparisonUnknown),
			errors.Is(err, threshold.ErrValueNotPositive),
			errors.Is(err, threshold.ErrValueOutOfRange):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			respondError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, toThresholdResponses(stored))
}

// thresholdResultResponse is one row of threshold_results on the run report:
// the definition snapshot as evaluated, what the report showed, and whether
// it met the bound. observed_value and satisfied are null when the report
// carried no figure for the metric -- unknown, never a fabricated fail --
// and reason says why. threshold_id rides along for callers that correlate
// against GET /api/scenarios/{id}/thresholds.
type thresholdResultResponse struct {
	ThresholdID   int64    `json:"threshold_id"`
	Metric        string   `json:"metric"`
	Comparison    string   `json:"comparison"`
	Value         float64  `json:"value"`
	ObservedValue *float64 `json:"observed_value"`
	Satisfied     *bool    `json:"satisfied"`
	Reason        string   `json:"reason,omitempty"`
}

func toThresholdResultResponses(list []threshold.Result) []thresholdResultResponse {
	out := make([]thresholdResultResponse, 0, len(list))
	for _, res := range list {
		out = append(out, thresholdResultResponse{
			ThresholdID:   res.ThresholdID,
			Metric:        string(res.Metric),
			Comparison:    string(res.Comparison),
			Value:         res.Value,
			ObservedValue: res.Observed,
			Satisfied:     res.Satisfied,
			Reason:        res.Reason,
		})
	}
	return out
}

// thresholdResultsForRun reads a run's stored threshold results for the
// report overlay: strictly additive, the criteria layer's own tolerance --
// no threshold service wired, or a read that fails, yields an empty array,
// never a failed report read. The report existed and was authorized before
// thresholds entered the picture.
func (h *handlers) thresholdResultsForRun(r *http.Request, runID int64) []thresholdResultResponse {
	if h.deps.Thresholds == nil {
		return []thresholdResultResponse{}
	}
	results, err := h.deps.Thresholds.ResultsForRun(r.Context(), runID)
	if err != nil {
		slog.Error("httpapi: threshold results read", "error", err)
		return []thresholdResultResponse{}
	}
	return toThresholdResultResponses(results)
}
