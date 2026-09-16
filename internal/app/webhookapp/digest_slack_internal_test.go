package webhookapp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
)

// digestWithThresholds marshals a real digestapp.Payload -- the bytes the
// digest lane actually stores and hands the Slack transport -- with the
// given per-run outcome spellings.
func digestWithThresholds(t *testing.T, outcomes ...string) []byte {
	t.Helper()
	p := digestapp.Payload{
		Event: digestapp.EventDigest, ProjectID: 5, Period: digest.PeriodDaily,
		WindowStart: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
		RunsTotal:   len(outcomes),
		Thresholds:  make([]digestapp.ThresholdLine, 0, len(outcomes)),
	}
	p.ByOutcome.Failed = 1
	p.ThresholdFailures = 1
	for i, outcome := range outcomes {
		p.Thresholds = append(p.Thresholds, digestapp.ThresholdLine{
			RunID: int64(i + 1), Outcome: outcome, Missed: []digestapp.ThresholdMiss{},
		})
	}
	raw, err := json.Marshal(&p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

// TestSlackDigestBodyThresholdLine pins the Slack summary's threshold line
// (phase 74): when the window graded runs, one line in the run-count line's
// own style -- 'Thresholds: N runs · X all-met · Y missed' -- slotted
// between the run counts and the details hint. Unknown verdicts count in N
// but neither bucket; a definitive miss outranks them, the same triage
// order the payload's outcome field encodes.
func TestSlackDigestBodyThresholdLine(t *testing.T) {
	msg, ok := slackDigestBody(digestWithThresholds(t,
		digestapp.ThresholdOutcomeMissed, digestapp.ThresholdOutcomeAllMet,
		digestapp.ThresholdOutcomeAllMet, digestapp.ThresholdOutcomeUnknown,
	))
	if !ok {
		t.Fatal("slackDigestBody: not ok, want the digest rendered")
	}
	var m struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatalf("decode slack body: %v (%s)", err, msg)
	}
	wantLine := "Thresholds: 4 runs · 2 all-met · 1 missed"
	if !strings.Contains(m.Text, wantLine) {
		t.Errorf("slack text = %q, want it to contain %q", m.Text, wantLine)
	}
	lines := strings.Split(m.Text, "\n")
	if len(lines) != 4 {
		t.Fatalf("slack text has %d lines, want 4 (header, runs, thresholds, details)", len(lines))
	}
	if !strings.HasPrefix(lines[3], "Details: /executions") {
		t.Errorf("last line = %q, want the details hint last", lines[3])
	}
}

// TestSlackDigestBodyNoThresholds pins both quiet cases: a payload from
// before the section existed (no thresholds key at all) and a current one
// whose window graded nothing render exactly the three-line summary they
// always did -- no "Thresholds: 0 runs" noise.
func TestSlackDigestBodyNoThresholds(t *testing.T) {
	legacy := []byte(`{"event":"report.digest","project_id":5,"period":"daily",
		"window_start":"2026-03-01T00:00:00Z","window_end":"2026-03-02T00:00:00Z",
		"runs_total":3,"by_outcome":{"passed":2,"failed":1,"aborted":0},
		"threshold_failures":1}`)
	msg, ok := slackDigestBody(legacy)
	if !ok {
		t.Fatal("slackDigestBody(legacy): not ok")
	}
	if strings.Contains(string(msg), "Thresholds:") {
		t.Errorf("legacy payload rendered a threshold line: %s", msg)
	}

	ungraded := digestWithThresholds(t) // zero graded runs, section present but empty
	msg, ok = slackDigestBody(ungraded)
	if !ok {
		t.Fatal("slackDigestBody(ungraded): not ok")
	}
	if strings.Contains(string(msg), "Thresholds:") {
		t.Errorf("ungraded window rendered a threshold line: %s", msg)
	}
	var m struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatalf("decode slack body: %v (%s)", err, msg)
	}
	if lines := strings.Split(m.Text, "\n"); len(lines) != 3 {
		t.Errorf("ungraded summary has %d lines, want the classic 3", len(lines))
	}
}
