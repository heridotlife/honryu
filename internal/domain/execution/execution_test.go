package execution_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/execution"
)

func TestNew_Valid(t *testing.T) {
	t.Parallel()

	c, err := execution.New("  peak-hour  ", 3)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if c.Name != "peak-hour" {
		t.Errorf("Name = %q, want trimmed peak-hour", c.Name)
	}
	if c.ProjectID != 3 {
		t.Errorf("ProjectID = %d, want 3", c.ProjectID)
	}
	if c.CSVSplit {
		t.Errorf("CSVSplit = true, want false by default")
	}
	if c.Kind != execution.KindNormal {
		t.Errorf("Kind = %q, want %q by default", c.Kind, execution.KindNormal)
	}
}

func TestNew_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		collName string
		project  int64
		wantErr  error
	}{
		{"empty name", "", 1, execution.ErrNameRequired},
		{"name too long", strings.Repeat("c", 101), 1, execution.ErrNameTooLong},
		{"zero project", "peak", 0, execution.ErrProjectRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := execution.New(tc.collName, tc.project)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("New(%q,%d) err = %v, want %v", tc.collName, tc.project, err, tc.wantErr)
			}
		})
	}
}

// An execution row persisted before Kind existed decodes to the Go zero
// value ("") -- Validate must keep treating that as valid (equivalent to
// KindNormal), the same tolerance it already gives an empty Engine.
func TestValidate_EmptyKindIsValid(t *testing.T) {
	t.Parallel()
	c := execution.Execution{Name: "peak", ProjectID: 1}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate (empty kind) = %v, want nil", err)
	}
}

func TestValidate_UnknownKindRejected(t *testing.T) {
	t.Parallel()
	c := execution.Execution{Name: "peak", ProjectID: 1, Kind: execution.Kind("bogus")}
	if err := c.Validate(); !errors.Is(err, execution.ErrKindUnknown) {
		t.Fatalf("Validate (unknown kind) = %v, want ErrKindUnknown", err)
	}
}

func TestValidate_CalibrateEngineKindAccepted(t *testing.T) {
	t.Parallel()
	c := execution.Execution{Name: "peak", ProjectID: 1, Kind: execution.KindCalibrateEngine}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate (CalibrateEngine kind) = %v, want nil", err)
	}
}

func TestKind_Known(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind execution.Kind
		want bool
	}{
		{execution.KindNormal, true},
		{execution.KindCalibrateEngine, true},
		{execution.Kind(""), false},
		{execution.Kind("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.kind.Known(); got != tc.want {
			t.Errorf("Kind(%q).Known() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// --- Fan-out (phase 88) ------------------------------------------------------

func TestNormalizeFanOutTargets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr error
	}{
		{"nil is no fan-out", nil, nil, nil},
		{"empty is no fan-out", []string{}, nil, nil},
		{"blank entries are dropped", []string{" a ", "", "  ", "b"}, []string{"a", "b"}, nil},
		{"all-blank is no fan-out", []string{"", "  "}, nil, nil},
		{"duplicates refused", []string{"a", "b", " a "}, nil, execution.ErrFanOutTargetDuplicate},
		{"duplicates after trim refused", []string{"a", "a"}, nil, execution.ErrFanOutTargetDuplicate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := execution.NormalizeFanOutTargets(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NormalizeFanOutTargets(%q) err = %v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeFanOutTargets(%q) = %v, want nil", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("NormalizeFanOutTargets(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("NormalizeFanOutTargets(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestValidate_DuplicateFanOutTargetsRejected(t *testing.T) {
	t.Parallel()
	c := execution.Execution{Name: "peak", ProjectID: 1, FanOutTargets: []string{"eu", "us", "eu"}}
	if err := c.Validate(); !errors.Is(err, execution.ErrFanOutTargetDuplicate) {
		t.Fatalf("Validate (duplicate fan-out target) = %v, want ErrFanOutTargetDuplicate", err)
	}
}

func TestIsFanOut(t *testing.T) {
	t.Parallel()
	cases := []struct {
		targets []string
		want    bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"eu"}, true},
		{[]string{"eu", "us"}, true},
	}
	for _, tc := range cases {
		c := execution.Execution{Name: "peak", ProjectID: 1, FanOutTargets: tc.targets}
		if got := c.IsFanOut(); got != tc.want {
			t.Errorf("IsFanOut(%q) = %v, want %v", tc.targets, got, tc.want)
		}
	}
}

// Clusters is the one iteration source for every per-cluster operation: the
// fan-out targets of a fan-out execution, the single configured cluster
// (empty = the deployment default) of an ordinary one.
func TestClusters(t *testing.T) {
	t.Parallel()
	fan := execution.Execution{Name: "peak", ProjectID: 1, FanOutTargets: []string{"eu", "us"}}
	if got := fan.Clusters(); len(got) != 2 || got[0] != "eu" || got[1] != "us" {
		t.Errorf("Clusters (fan-out) = %q, want [eu us]", got)
	}
	single := execution.Execution{Name: "peak", ProjectID: 1, Cluster: "prod-eu"}
	if got := single.Clusters(); len(got) != 1 || got[0] != "prod-eu" {
		t.Errorf("Clusters (named cluster) = %q, want [prod-eu]", got)
	}
	def := execution.Execution{Name: "peak", ProjectID: 1}
	if got := def.Clusters(); len(got) != 1 || got[0] != "" {
		t.Errorf("Clusters (default) = %q, want the empty default ref [\"\"]", got)
	}
}
