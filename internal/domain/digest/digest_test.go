package digest

import (
	"errors"
	"testing"
	"time"
)

func TestParsePeriod(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]Period{
		"daily":  PeriodDaily,
		"weekly": PeriodWeekly,
	} {
		got, err := ParsePeriod(raw)
		if err != nil {
			t.Errorf("ParsePeriod(%q) = %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParsePeriod(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"", "hourly", "DAILY", "daily "} {
		if _, err := ParsePeriod(raw); !errors.Is(err, ErrPeriodInvalid) {
			t.Errorf("ParsePeriod(%q) = %v, want ErrPeriodInvalid", raw, err)
		}
	}
}

func TestPeriodDuration(t *testing.T) {
	t.Parallel()
	if got := PeriodDaily.Duration(); got != 24*time.Hour {
		t.Errorf("daily = %v, want 24h", got)
	}
	if got := PeriodWeekly.Duration(); got != 7*24*time.Hour {
		t.Errorf("weekly = %v, want 168h", got)
	}
}
