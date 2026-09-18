package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newExecutionRouter wires just enough of the API to exercise execution
// creation: a project service and an execution service over the in-memory
// store (legacy auth mode, like newAdminRouter but without the admin surface).
func newExecutionRouter(t *testing.T) http.Handler {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	return httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Executions:    executionapp.NewService(store, obj, 100),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
}

// A fan-out execution is created by POSTing fanout_targets as a JSON array of
// cluster names; the created row (and both list shapes) carries them back.
func TestCreateExecution_FanOutTargets(t *testing.T) {
	t.Parallel()
	h := newExecutionRouter(t)

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	rec := postForm(t, h, "/api/executions", url.Values{
		"name":           {"everywhere"},
		"project_id":     {itoa(projectID)},
		"fanout_targets": {`["eu-1","us-1"]`},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/executions status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var created struct {
		ID            int64    `json:"id"`
		FanOutTargets []string `json:"fanout_targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if len(created.FanOutTargets) != 2 || created.FanOutTargets[0] != "eu-1" || created.FanOutTargets[1] != "us-1" {
		t.Fatalf("fanout_targets = %q, want [eu-1 us-1]", created.FanOutTargets)
	}

	// The list shape carries them too -- the Clusters page and execution
	// lists fan out off this field without a per-row detail fetch.
	list := do(t, h, http.MethodGet, "/api/executions")
	if list.Code != http.StatusOK {
		t.Fatalf("GET /api/executions status = %d", list.Code)
	}
	var summaries []struct {
		ID            int64    `json:"id"`
		FanOutTargets []string `json:"fanout_targets"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode list: %v (%s)", err, list.Body.String())
	}
	if len(summaries) != 1 || len(summaries[0].FanOutTargets) != 2 {
		t.Fatalf("list fanout_targets = %+v, want one row with two targets", summaries)
	}
}

// An absent or blank fanout_targets field stays an ordinary execution -- the
// exact request every pre-fan-out client sends must keep working, and the
// response must not grow an empty array where none existed before.
func TestCreateExecution_NoFanOutTargetsOmitted(t *testing.T) {
	t.Parallel()
	h := newExecutionRouter(t)

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	rec := postForm(t, h, "/api/executions", url.Values{
		"name":       {"plain"},
		"project_id": {itoa(projectID)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/executions status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if field, ok := rawField(rec.Body.String(), "fanout_targets"); ok {
		t.Fatalf("fanout_targets = %q on an ordinary execution, want absent", field)
	}
}

// rawField reports whether the JSON body carries key, and its raw value
// when it does -- an omitempty-shape assertion without decoding into a map
// of floats.
func rawField(body, key string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return nil, false
	}
	raw, ok := m[key]
	return raw, ok
}

// A present-but-malformed fanout_targets field is a client error, not a
// silently-ignored typo: the caller asked for fan-out and got single-cluster.
func TestCreateExecution_FanOutTargetsMalformed(t *testing.T) {
	t.Parallel()
	h := newExecutionRouter(t)

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	for _, raw := range []string{`"eu-1"`, `{}`, `eu-1`, `[1,2]`} {
		rec := postForm(t, h, "/api/executions", url.Values{
			"name":           {"everywhere"},
			"project_id":     {itoa(projectID)},
			"fanout_targets": {raw},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/executions fanout_targets=%q status = %d, want 400", raw, rec.Code)
		}
	}
}

// A duplicate target is refused with the domain's typed error.
func TestCreateExecution_FanOutTargetsDuplicate(t *testing.T) {
	t.Parallel()
	h := newExecutionRouter(t)

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	rec := postForm(t, h, "/api/executions", url.Values{
		"name":           {"everywhere"},
		"project_id":     {itoa(projectID)},
		"fanout_targets": {`["eu-1","eu-1"]`},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/executions (duplicate target) status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}
