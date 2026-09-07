package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/clusterapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newCapacityRouter builds a clusters router over the fake store, seeding
// two tenants' ceilings (4 + 8 = 12 aggregate) on the default cluster.
func newCapacityRouter(withQuota bool) http.Handler {
	store := fake.NewStore()
	_ = store.SetCeiling(context.Background(), 1, "honryu", 4)
	_ = store.SetCeiling(context.Background(), 4, "honryu", 8)
	deps := httpapi.Deps{
		Clusters: &stubListClusters{rows: []clusterregistry.Cluster{{Name: "honryu", Origin: clusterregistry.OriginOperator}}},
		// no-auth: the admin gate passes on rbac-disabled
		DefaultOwners: []string{"honryu"},
	}
	if withQuota {
		deps.Quota = quotaapp.NewService(store)
	}
	return httpapi.NewRouter(deps)
}

// stubListClusters satisfies httpapi.ClusterService for list-only tests.
type stubListClusters struct {
	rows []clusterregistry.Cluster
}

func (s *stubListClusters) RegisterOperator(_ context.Context, e clusterregistry.Cluster) (clusterregistry.Cluster, error) {
	return e, nil
}
func (s *stubListClusters) RegisterBYOC(_ context.Context, e clusterregistry.Cluster, _ []byte) (clusterapp.RegisterResult, error) {
	return clusterapp.RegisterResult{}, nil
}
func (s *stubListClusters) RotateIngestToken(_ context.Context, _ string) (string, error) {
	return "tok", nil
}
func (s *stubListClusters) Get(_ context.Context, _ string) (clusterregistry.Cluster, error) {
	return s.rows[0], nil
}
func (s *stubListClusters) List(_ context.Context) ([]clusterregistry.Cluster, error) {
	return s.rows, nil
}
func (s *stubListClusters) Update(_ context.Context, _ string, _, _, _ string) (clusterregistry.Cluster, error) {
	return s.rows[0], nil
}
func (s *stubListClusters) Delete(_ context.Context, _ string) error { return nil }

// TestListClusters_WithoutQuotaDepOmitsCapacityFields pins the wire
// contract: nil Quota dep keeps /api/clusters bodies free of
// engines_used/engines_ceiling entirely (absence, not zero) -- a deployment
// without the ledger is byte-identical to before phase 25.
func TestListClusters_WithoutQuotaDepOmitsCapacityFields(t *testing.T) {
	t.Parallel()
	h := newCapacityRouter(false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clusters", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "engines_used") || strings.Contains(body, "engines_ceiling") {
		t.Fatalf("nil quota dep must omit capacity fields, got: %s", body)
	}
}

// TestListClusters_WithQuotaDepCarriesCapacity: ceilings summed across
// tenants (4+8=12) on the wire, used present (0: no reservations).
func TestListClusters_WithQuotaDepCarriesCapacity(t *testing.T) {
	t.Parallel()
	h := newCapacityRouter(true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clusters", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 cluster, got %d", len(out))
	}
	c := out[0]
	ceil, okC := c["engines_ceiling"].(float64)
	used, okU := c["engines_used"].(float64)
	if !okC || !okU {
		t.Fatalf("capacity fields missing: %v", c)
	}
	if ceil != 12 {
		t.Errorf("engines_ceiling = %v, want 12 (4+8)", ceil)
	}
	if used != 0 {
		t.Errorf("engines_used = %v, want 0 (no reservations)", used)
	}
}
