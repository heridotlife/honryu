package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/domain/run"
)

// respondError's details envelope, driven directly: tenant_quota_test.go
// covers the full wiring; these pin the hint variants (set vs
// unconfigured ceiling) and the bare-sentinel fallback without building a
// router.

// quotaDetailsBody mirrors the 429 envelope respondError emits.
type quotaDetailsBody struct {
	Message string `json:"message"`
	Details struct {
		TenantID  int64  `json:"tenant_id"`
		Cluster   string `json:"cluster"`
		Requested int    `json:"requested"`
		Used      int    `json:"used"`
		Ceiling   int    `json:"ceiling"`
		Hint      string `json:"hint"`
	} `json:"details"`
}

func TestRespondError_OverQuotaDetails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		wantUsed int
		wantCeil int
		wantHint string
	}{
		{
			name:     "configured ceiling",
			err:      &quotaapp.OverQuotaError{TenantID: 7, Cluster: "prod", Requested: 4, Used: 6, Ceiling: 9},
			wantUsed: 6,
			wantCeil: 9,
			wantHint: "PUT /api/tenants/{tenant_id}/quota ceiling=9",
		},
		{
			// Phase 104: a ceiling of 0 can only be an explicitly configured
			// block (missing rows read the platform default), so the hint
			// says the ceiling is set to 0 rather than that no row exists.
			name:     "explicit zero ceiling",
			err:      &quotaapp.OverQuotaError{TenantID: 7, Cluster: "prod", Requested: 4, Used: 0, Ceiling: 0, ZeroCeiling: true},
			wantUsed: 0,
			wantCeil: 0,
			wantHint: "PUT /api/tenants/{tenant_id}/quota ceiling=0; ceiling is set to 0 for this tenant+cluster",
		},
		{
			// Callers re-wrap (Trigger's own %w chains); the typed form must
			// survive the wrap for the details to survive it too.
			name:     "wrapped",
			err:      fmt.Errorf("trigger: %w", &quotaapp.OverQuotaError{TenantID: 7, Cluster: "prod", Requested: 4, Used: 6, Ceiling: 9}),
			wantUsed: 6,
			wantCeil: 9,
			wantHint: "PUT /api/tenants/{tenant_id}/quota ceiling=9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			respondError(rec, tc.err)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", rec.Code)
			}
			var body quotaDetailsBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("429 body is not the envelope: %v (%s)", err, rec.Body.String())
			}
			if body.Message != "reservation would exceed tenant quota" {
				t.Fatalf("message = %q, want the fixed contract", body.Message)
			}
			d := body.Details
			if d.TenantID != 7 || d.Cluster != "prod" || d.Requested != 4 || d.Used != tc.wantUsed || d.Ceiling != tc.wantCeil {
				t.Fatalf("details = %+v, want the typed error's numbers", d)
			}
			if d.Hint != tc.wantHint {
				t.Fatalf("hint = %q, want %q", d.Hint, tc.wantHint)
			}
		})
	}
}

// A bare sentinel (no typed form attached) keeps the message-only envelope:
// "details" is opt-in per error, never an empty object.
func TestRespondError_OverQuotaSentinelWithoutNumbersIsMessageOnly(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	respondError(rec, quotaapp.ErrOverQuota)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"reservation would exceed tenant quota"}` {
		t.Fatalf("bare-sentinel 429 body = %s, want message-only", got)
	}
}

// writeError delegates to writeErrorDetails with nil details, and nil must
// omit the key: message-only bodies stay byte-identical to the
// pre-envelope shape existing consumers pin.
func TestWriteErrorDetails_NilOmitsTheKey(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeErrorDetails(rec, http.StatusNotFound, "ports: not found", nil)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"ports: not found"}` {
		t.Fatalf("nil-details body = %s, want message-only byte-identical", got)
	}
}

// The engines-finished conflict (409) carries the orphan count and the
// purge-and-redeploy remediation in the details envelope, on top of the
// verbatim conflict message -- and the typed form survives a re-wrap.
func TestRespondError_EnginesFinishedDetails(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	respondError(rec, fmt.Errorf("trigger: %w", &lifecycleapp.EnginesFinishedError{Orphaned: 3}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Message string `json:"message"`
		Details struct {
			OrphanedCompletions int    `json:"orphaned_completions"`
			Hint                string `json:"hint"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("409 body is not the envelope: %v (%s)", err, rec.Body.String())
	}
	const wantMsg = "run: engines already finished, redeploy before triggering: 3 orphaned shard completion(s)"
	if body.Message != wantMsg {
		t.Fatalf("message = %q, want the verbatim conflict text %q", body.Message, wantMsg)
	}
	if body.Details.OrphanedCompletions != 3 {
		t.Fatalf("orphaned_completions = %d, want 3", body.Details.OrphanedCompletions)
	}
	if body.Details.Hint != "purge the execution and redeploy before triggering" {
		t.Fatalf("hint = %q, want the purge+redeploy remediation", body.Details.Hint)
	}
}

// A bare run.ErrEnginesFinished keeps the message-only 409: the details
// envelope is opt-in per error, never an empty object.
func TestRespondError_EnginesFinishedSentinelIsMessageOnly(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	respondError(rec, run.ErrEnginesFinished)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"run: engines already finished, redeploy before triggering"}` {
		t.Fatalf("bare-sentinel 409 body = %s, want message-only", got)
	}
}
