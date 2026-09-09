package telemetry_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/telemetry"
)

func tenant(id int64) *int64 { return &id }

func TestHeaders_RendersW3CTraceparentAndBaggage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		id      telemetry.Identity
		baggage string
	}{
		{
			name: "full identity",
			id: telemetry.Identity{
				TenantID:         tenant(7),
				ProjectID:        42,
				ExecutionID:      99,
				RunCorrelationID: "4bf92f3577b34da6a3ce929d0e0e4736",
			},
			baggage: "honryu.tenant=7,honryu.service=42,honryu.execution=99,honryu.run=4bf92f3577b34da6a3ce929d0e0e4736",
		},
		{
			// No tenant means no honryu.tenant entry at all -- rendering an
			// empty value would make every APM query for honryu.tenant="" the
			// way to find tenant-less runs.
			name: "nil tenant omits the entry rather than rendering it empty",
			id: telemetry.Identity{
				ProjectID:        42,
				ExecutionID:      99,
				RunCorrelationID: "4bf92f3577b34da6a3ce929d0e0e4736",
			},
			baggage: "honryu.service=42,honryu.execution=99,honryu.run=4bf92f3577b34da6a3ce929d0e0e4736",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc := tc
			h := telemetry.Headers(telemetry.TraceContext{
				TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
				// The example parent id from the W3C trace context spec.
				ParentID: "00f067aa0ba902b7",
			}, tc.id)

			if len(h) != 2 {
				t.Fatalf("got %d headers (%v), want exactly traceparent and baggage", len(h), h)
			}
			if got, want := h["traceparent"], "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"; got != want {
				t.Errorf("traceparent = %q, want %q", got, want)
			}
			if got := h["baggage"]; got != tc.baggage {
				t.Errorf("baggage = %q, want %q", got, tc.baggage)
			}
		})
	}
}

// The sampled flag is always 00, with no override path: sampling a load test's
// traffic on would distort the very load being measured. The renderer must not
// be able to express anything else.
func TestHeaders_SampledFlagIsAlwaysOff(t *testing.T) {
	t.Parallel()

	h := telemetry.Headers(telemetry.TraceContext{TraceID: strings.Repeat("a", 32), ParentID: strings.Repeat("b", 16)}, telemetry.Identity{})
	tp, ok := h["traceparent"]
	if !ok {
		t.Fatal("no traceparent header")
	}
	if !strings.HasSuffix(tp, "-00") {
		t.Errorf("traceparent %q does not end in the unsampled flags value 00", tp)
	}
}

// The run's correlation id is the traceparent trace id -- one generated value,
// not two identifiers that could drift apart.
func TestHeaders_RunBaggageEntryIsTheTraceID(t *testing.T) {
	t.Parallel()

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	h := telemetry.Headers(
		telemetry.TraceContext{TraceID: traceID, ParentID: "00f067aa0ba902b7"},
		telemetry.Identity{RunCorrelationID: traceID},
	)
	if !strings.Contains(h["baggage"], "honryu.run="+traceID) {
		t.Errorf("baggage %q does not carry honryu.run=%s", h["baggage"], traceID)
	}
	if !strings.Contains(h["traceparent"], traceID) {
		t.Errorf("traceparent %q does not carry the trace id %s", h["traceparent"], traceID)
	}
}

// Same input, same bytes: the rendered headers are what gets diffed to prove
// every scenario and shard of one deploy carried an identical pair.
func TestHeaders_IsDeterministic(t *testing.T) {
	t.Parallel()

	tc := telemetry.TraceContext{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", ParentID: "00f067aa0ba902b7"}
	id := telemetry.Identity{TenantID: tenant(7), ProjectID: 42, ExecutionID: 99, RunCorrelationID: "4bf92f3577b34da6a3ce929d0e0e4736"}

	first := telemetry.Headers(tc, id)
	second := telemetry.Headers(tc, id)
	for key := range first {
		if first[key] != second[key] {
			t.Errorf("%s rendered %q then %q", key, first[key], second[key])
		}
	}
}

// A well-formed trace id is 32 lowercase hex and a parent id 16, per the W3C
// trace context spec; the e2e and live verification both check for this shape.
func TestHeaders_WellFormedIds(t *testing.T) {
	t.Parallel()

	h := telemetry.Headers(telemetry.TraceContext{TraceID: "0123456789abcdef0123456789abcdef", ParentID: "0123456789abcdef"}, telemetry.Identity{})
	tp := h["traceparent"]

	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		t.Fatalf("traceparent %q is not four dash-separated fields", tp)
	}
	traceIDRe := regexp.MustCompile(`^[0-9a-f]{32}$`)
	parentIDRe := regexp.MustCompile(`^[0-9a-f]{16}$`)
	if !traceIDRe.MatchString(parts[1]) {
		t.Errorf("trace id %q is not 32 lowercase hex", parts[1])
	}
	if !parentIDRe.MatchString(parts[2]) {
		t.Errorf("parent id %q is not 16 lowercase hex", parts[2])
	}
}

// Baggage is Headers' baggage half on its own (phase 37): the report surface
// renders it from the stored identity alone, so it must match what Headers
// put on the wire at deploy time -- same entries, same order, tenant entry
// omitted (never emptied) without a tenant.
func TestBaggage_MatchesHeaders(t *testing.T) {
	t.Parallel()

	withTenant := telemetry.Identity{
		TenantID:         tenant(7),
		ProjectID:        42,
		ExecutionID:      99,
		RunCorrelationID: "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	withoutTenant := withTenant
	withoutTenant.TenantID = nil

	for _, id := range []telemetry.Identity{withTenant, withoutTenant} {
		if got, want := telemetry.Baggage(id), telemetry.Headers(telemetry.TraceContext{TraceID: "x", ParentID: "y"}, id)["baggage"]; got != want {
			t.Errorf("Baggage(%+v) = %q, want the Headers baggage half %q", id, got, want)
		}
	}
	if got, want := telemetry.Baggage(withoutTenant), "honryu.service=42,honryu.execution=99,honryu.run=4bf92f3577b34da6a3ce929d0e0e4736"; got != want {
		t.Errorf("Baggage without tenant = %q, want %q", got, want)
	}
}
