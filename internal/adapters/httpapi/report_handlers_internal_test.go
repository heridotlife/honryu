package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestParseBaselineRunID pins the optional compare-baseline param's grammar
// (phase 75) at the parser level: absent, empty, or whitespace-only means
// "order as asked"; a valid id is tolerated with surrounding whitespace; and
// anything non-numeric, non-positive, or overflowing int64 is the caller's
// mistake, named as such in the message.
func TestParseBaselineRunID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		query   string
		wantID  int64
		wantErr string
	}{
		{name: "absent", query: "", wantID: 0},
		{name: "empty", query: "baseline_run_id=", wantID: 0},
		{name: "whitespace only", query: "baseline_run_id=%20%20", wantID: 0},
		{name: "valid", query: "baseline_run_id=42", wantID: 42},
		{name: "valid with surrounding whitespace", query: "baseline_run_id=%0942%20", wantID: 42},
		{name: "non-numeric", query: "baseline_run_id=dave", wantErr: `invalid baseline_run_id "dave"`},
		{name: "float", query: "baseline_run_id=1.5", wantErr: `invalid baseline_run_id "1.5"`},
		{name: "negative", query: "baseline_run_id=-1", wantErr: `invalid baseline_run_id "-1"`},
		{name: "zero", query: "baseline_run_id=0", wantErr: `invalid baseline_run_id "0"`},
		{name: "int64 overflow", query: "baseline_run_id=9223372036854775808", wantErr: `invalid baseline_run_id "9223372036854775808"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/api/runs/compare?"+tc.query, nil)
			id, err := parseBaselineRunID(r)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parse %q = %d, nil; want error", tc.query, id)
				}
				if id != 0 {
					t.Errorf("parse %q id = %d on error, want 0", tc.query, id)
				}
				if err.Error() != tc.wantErr {
					t.Errorf("parse %q err = %q, want %q", tc.query, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tc.query, err)
			}
			if id != tc.wantID {
				t.Errorf("parse %q = %d, want %d", tc.query, id, tc.wantID)
			}
		})
	}
}
