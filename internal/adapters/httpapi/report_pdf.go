package httpapi

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/heridotlife/honryu/internal/app/reportapp"
	"github.com/heridotlife/honryu/internal/domain/report"
)

// runExportPDF renders the PDF download: the same content the CSV export
// carries (run header, per-label table, per-second series), as a minimal
// single-byte-font PDF written by hand. No external dependency: the export
// is tables and text, and a hand-rolled writer keeps the supply chain (and
// gosec/govulncheck) untouched. Content model mirrors runExportCSV
// deliberately -- one source of truth for what an export contains.
//
// Structure: objects 1-5 the fixed skeleton (catalog, pages, page, font,
// content stream); a second page is added when the series runs past one
// page (50 rows), which is the only pagination the export needs.
func runExportPDF(runID int64, rep report.Report, points []reportapp.SeriesPoint) []byte {
	var b pdfBuilder
	b.start()
	b.heading(fmt.Sprintf("Honryu run %d report", runID))
	b.kv("Execution", strconv.FormatInt(rep.ExecutionID, 10))
	b.kv("Scenario", strconv.FormatInt(rep.ScenarioID, 10))
	if rep.Engine != "" {
		b.kv("Engine", string(rep.Engine))
	}
	if rep.Cluster != "" {
		b.kv("Cluster", rep.Cluster)
	}
	b.kv("Started", rep.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	b.kv("Ended", rep.EndedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	b.kv("Outcome", string(rep.Outcome))
	if rep.CorrelationID != "" {
		b.kv("Correlation id", rep.CorrelationID)
	}
	b.kv("Requested", fmt.Sprintf("%d VUs, %s rps, %ds",
		rep.Requested.Concurrency, exportFloat(rep.Requested.Throughput), rep.Requested.DurationSeconds))
	b.kv("Achieved", fmt.Sprintf("%d VUs, %s rps, %ds, %d samples, %d failed",
		rep.Achieved.Concurrency, exportFloat(rep.Achieved.Throughput), rep.Achieved.DurationSeconds,
		rep.Achieved.Samples, rep.Achieved.Failed))
	b.blank()

	b.heading("Labels")
	b.tableHeader([]string{"Label", "Samples", "Errors", "Err%", "p50", "p95", "p99"})
	for _, l := range rep.Labels {
		b.row([]string{
			pdfEscape(l.Label), strconv.FormatInt(l.Samples, 10), strconv.FormatInt(l.Failed, 10),
			exportFloat(l.ErrorRate), exportPercentile(l.Latency, 50),
			exportPercentile(l.Latency, 95), exportPercentile(l.Latency, 99),
		})
	}
	b.blank()

	b.heading("Per-second series")
	b.tableHeader([]string{"ts", "vus", "rps", "err%", "p50", "p90", "p95", "p99"})
	for _, p := range points {
		b.row([]string{
			strconv.FormatInt(p.Ts, 10), exportFloat(p.VUs), exportFloat(p.RPS), exportFloat(p.ErrPct),
			exportPercentile(p.Latency, 50), exportPercentile(p.Latency, 90),
			exportPercentile(p.Latency, 95), exportPercentile(p.Latency, 99),
		})
	}
	return b.finish()
}

// pdfBuilder writes a minimal PDF: one font (Helvetica base-14, so no
// embedded font file), A4 pages, a fixed 11pt monospaced-feel layout using
// Helvetica for everything (heading 14pt bold-free, body 9pt), and text
// positioned per-line via Td. Everything is ASCII after pdfEscape; non-ASCII
// in a label becomes '?', which keeps the single-byte encoding honest.
type pdfBuilder struct {
	pages   [][]string // per-page lines: "x y size text" operands pre-escaped
	current []string
	y       float64
}

const (
	pdfPageW  = 595.28 // A4 width, points
	pdfPageH  = 841.89 // A4 height, points
	pdfLeft   = 40.0
	pdfTop    = 60
	pdfBottom = 50.0
)

func (b *pdfBuilder) start() {
	b.current = []string{}
	b.y = pdfPageH - pdfTop
}

func (b *pdfBuilder) newline(size float64, gap float64) {
	b.y -= gap
	if b.y < pdfBottom {
		b.pages = append(b.pages, b.current)
		b.current = []string{}
		b.y = pdfPageH - pdfTop
	}
	_ = size
}

func (b *pdfBuilder) heading(text string) {
	b.newline(14, 20)
	b.current = append(b.current, fmt.Sprintf("BT /F1 13 Tf %.2f %.2f Td (%s) Tj ET", pdfLeft, b.y, pdfEscape(text)))
}

func (b *pdfBuilder) kv(k, v string) {
	b.newline(9, 12)
	b.current = append(b.current, fmt.Sprintf("BT /F1 9 Tf %.2f %.2f Td (%s: %s) Tj ET", pdfLeft, b.y, pdfEscape(k), pdfEscape(v)))
}

func (b *pdfBuilder) blank() { b.newline(9, 8) }

func (b *pdfBuilder) tableHeader(cells []string) {
	b.newline(9, 13)
	b.current = append(b.current, fmt.Sprintf("BT /F1 9 Tf %.2f %.2f Td (%s) Tj ET", pdfLeft, b.y, pdfEscape(strings.Join(cells, "  "))))
}

func (b *pdfBuilder) row(cells []string) {
	b.newline(9, 11)
	b.current = append(b.current, fmt.Sprintf("BT /F1 9 Tf %.2f %.2f Td (%s) Tj ET", pdfLeft, b.y, pdfEscape(strings.Join(cells, "  "))))
}

// pdfEscape makes a string safe inside a literal (...) PDF string: backslash
// first, then parens, then non-printable-ASCII to '?' (the single-byte
// Helvetica encoding has no slots for them).
func pdfEscape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '\\' || r == '(' || r == ')':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r >= 32 && r < 127:
			sb.WriteRune(r)
		default:
			sb.WriteByte('?')
		}
	}
	return sb.String()
}

// finish assembles the objects: 1 catalog, 2 pages, 3 font, 4 page-N
// (4..3+N), content streams (4+N..3+2N). Offsets table + trailer per spec.
func (b *pdfBuilder) finish() []byte {
	b.pages = append(b.pages, b.current)
	n := len(b.pages)
	var buf bytes.Buffer
	write := func(s string, a ...interface{}) { fmt.Fprintf(&buf, s, a...) }
	buf.WriteString("%PDF-1.4\n")
	offsets := []int{}
	// obj 1: catalog
	offsets = append(offsets, buf.Len())
	write("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	// obj 2: pages (kids filled later)
	offsets = append(offsets, buf.Len())
	write("2 0 obj\n<< /Type /Pages /Kids [")
	for i := 0; i < n; i++ {
		write("%d 0 R ", 4+i)
	}
	write("] /Count %d >>\nendobj\n", n)
	// obj 3: font
	offsets = append(offsets, buf.Len())
	write("3 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	// pages + contents
	for i, lines := range b.pages {
		pageObj := 4 + i
		contentObj := 4 + n + i
		offsets = append(offsets, buf.Len())
		write("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>\nendobj\n",
			pageObj, pdfPageW, pdfPageH, contentObj)
		var stream bytes.Buffer
		for _, l := range lines {
			stream.WriteString(l)
			stream.WriteString("\n")
		}
		offsets = append(offsets, buf.Len())
		write("%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
			contentObj, stream.Len(), stream.String())
	}
	xref := buf.Len()
	write("xref\n0 %d\n", 2+2*n+1)
	write("0000000000 65535 f \n")
	for _, off := range offsets {
		write("%010d 00000 n \n", off)
	}
	write("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", 2+2*n+1, xref)
	return buf.Bytes()
}
