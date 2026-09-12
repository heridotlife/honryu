package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
)

// Phase 37: GET /api/apm-links serves the deployment's link-out templates
// verbatim (placeholders intact -- the frontend substitutes per run) and
// always as an array, so "none configured" and "endpoint broken" are never
// the same wire shape.
func TestAPMLinks_ServesConfiguredTemplates(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{
		APMLinks: []httpapi.APMLinkTemplate{
			{Name: "Grafana Tempo", URLTemplate: "https://g.example.com/t/{{correlation_id}}?r={{run_id}}"},
			{Name: "Static wiki", URLTemplate: "https://wiki.example.com/runs"},
		},
	})

	rec := do(t, h, http.MethodGet, "/api/apm-links")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET apm-links = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("links = %d, want 2 (%s)", len(got), rec.Body.String())
	}
	if got[0]["name"] != "Grafana Tempo" || got[0]["url_template"] != "https://g.example.com/t/{{correlation_id}}?r={{run_id}}" {
		t.Errorf("link 0 = %v, want the template verbatim (placeholders intact)", got[0])
	}
}

// No templates configured is the default install, not an error: the array is
// empty and run pages render no link-outs.
func TestAPMLinks_EmptyByDefault(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{})

	rec := do(t, h, http.MethodGet, "/api/apm-links")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET apm-links = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("links = %v, want empty (never null)", got)
	}
}

// TestAPMLinks_EmptyBodyIsLiteralArray closes the gap the length check
// above cannot: json.Unmarshal leaves a nil slice untouched when the body
// is the JSON literal null, so len(got)==0 passes for null and [] alike --
// precisely the pair "no link-outs" and "endpoint broken" must stay
// apart on the wire (phase 37's array guarantee). The unconfigured body
// must be the literal [] bytes and decode to a non-nil empty slice.
func TestAPMLinks_EmptyBodyIsLiteralArray(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{})

	rec := do(t, h, http.MethodGet, "/api/apm-links")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET apm-links = %d (%s)", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("empty body = %q, want the literal [] (never null)", body)
	}
	var got []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got == nil {
		t.Fatal("decoded slice is nil, want a non-nil empty array")
	}
}
