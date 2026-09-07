package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// The PDF export: application/pdf content type, a %PDF magic prefix, the
// run's section headings present as text, and a well-formed trailer. Uses
// the same seeded environment the JSON/CSV export tests use (run 42).
func TestRunExport_PDFIsDownloadablePDF(t *testing.T) {
	t.Parallel()
	h, reports, progress := newSeriesEnv(t)
	seedExportRun(t, h, reports, progress)
	rec := do(t, h, http.MethodGet, "/api/runs/42/export?format=pdf")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export pdf = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="run-42.pdf"` {
		t.Errorf("Content-Disposition = %q, want run-42.pdf attachment", cd)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "%PDF-") {
		t.Fatalf("body does not start with %%PDF- magic: %q", body[:16])
	}
	for _, want := range []string{"Honryu run 42", "Labels", "Per-second series", "checkout", "startxref", "%%EOF"} {
		if !strings.Contains(body, want) {
			t.Errorf("pdf body missing %q", want)
		}
	}
	// The seeded label "checkout" must be inside a string literal, not able
	// to break out: no unescaped parens from the escape path (labels with
	// metacharacters route through pdfEscape).
	if strings.Contains(body, "(())") {
		t.Errorf("suspicious double-parens in pdf text")
	}
}

func TestRunExport_PDFTrailerWellFormed(t *testing.T) {
	t.Parallel()
	h, reports, progress := newSeriesEnv(t)
	seedExportRun(t, h, reports, progress)
	rec := do(t, h, http.MethodGet, "/api/runs/42/export?format=pdf")
	if rec.Code != http.StatusOK {
		t.Fatalf("pdf trailer = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	trimmed := strings.TrimRight(body, "\n")
	if !strings.HasSuffix(trimmed, "%%EOF") {
		t.Fatalf("pdf must end with %%%%EOF, got %q", trimmed[len(trimmed)-12:])
	}
	if !strings.Contains(body, "/Count 1") {
		t.Errorf("single-page pdf should declare /Count 1")
	}
}
