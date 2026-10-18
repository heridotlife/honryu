package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/clusterapp"
	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/ports"
)

// stubClusterService is a configurable httpapi.ClusterService for driving each
// handler path and error mapping without the full clusterapp wiring.
type stubClusterService struct {
	registerOperator  func(clusterregistry.Cluster) (clusterregistry.Cluster, error)
	registerBYOC      func(clusterregistry.Cluster, []byte) (clusterapp.RegisterResult, error)
	rotateIngestToken func(string) (string, error)
	get               func(string) (clusterregistry.Cluster, error)
	list              func() ([]clusterregistry.Cluster, error)
	update            func(name, ingest, sidecar, ns, geo, zone string) (clusterregistry.Cluster, error)
	del               func(string) error
}

func (s *stubClusterService) RegisterOperator(_ context.Context, e clusterregistry.Cluster) (clusterregistry.Cluster, error) {
	return s.registerOperator(e)
}
func (s *stubClusterService) RegisterBYOC(_ context.Context, e clusterregistry.Cluster, kc []byte) (clusterapp.RegisterResult, error) {
	return s.registerBYOC(e, kc)
}
func (s *stubClusterService) RotateIngestToken(_ context.Context, name string) (string, error) {
	return s.rotateIngestToken(name)
}
func (s *stubClusterService) Get(_ context.Context, name string) (clusterregistry.Cluster, error) {
	return s.get(name)
}
func (s *stubClusterService) List(_ context.Context) ([]clusterregistry.Cluster, error) {
	return s.list()
}
func (s *stubClusterService) Update(_ context.Context, name, ingest, sidecar, ns, geo, zone string) (clusterregistry.Cluster, error) {
	return s.update(name, ingest, sidecar, ns, geo, zone)
}
func (s *stubClusterService) Delete(_ context.Context, name string) error { return s.del(name) }

func newClusterRouter(svc httpapi.ClusterService) http.Handler {
	return httpapi.NewRouter(httpapi.Deps{
		Clusters:      svc,
		DefaultOwners: []string{"honryu"}, // no-auth: the admin gate passes on rbac-disabled
	})
}

func doForm(t *testing.T, h http.Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateCluster_BYOC_HappyPath(t *testing.T) {
	t.Parallel()
	var gotKubeconfig []byte
	svc := &stubClusterService{
		registerBYOC: func(e clusterregistry.Cluster, kc []byte) (clusterapp.RegisterResult, error) {
			gotKubeconfig = kc
			e.Origin = clusterregistry.OriginBYOC
			e.APIURL = "https://byoc:6443"
			e.SecretRef = "honryu-cluster-" + e.Name
			return clusterapp.RegisterResult{Cluster: e, IngestToken: "tok-once"}, nil
		},
	}
	h := newClusterRouter(svc)

	rec := doForm(t, h, http.MethodPost, "/api/clusters", url.Values{
		"name": {"prod-eu"}, "ingest_url": {"http://ingest"}, "sidecar_image": {"img"},
		"namespace": {"honryu"}, "kubeconfig": {"apiVersion: v1\nkind: Config"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create BYOC = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if string(gotKubeconfig) != "apiVersion: v1\nkind: Config" {
		t.Fatalf("kubeconfig not forwarded: %q", gotKubeconfig)
	}
	if !strings.Contains(rec.Body.String(), `"origin":"byoc"`) {
		t.Fatalf("response missing byoc origin: %s", rec.Body.String())
	}
	// The minted token rides the registration response exactly once.
	if !strings.Contains(rec.Body.String(), `"ingest_token":"tok-once"`) {
		t.Fatalf("response missing the one-time ingest token: %s", rec.Body.String())
	}
}

// Rotation is the token's lifecycle: a new one is returned once, the old one
// is dead server-side. Not-found maps to the usual 404.
func TestRotateIngestToken(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{
		rotateIngestToken: func(name string) (string, error) {
			if name != "prod-eu" {
				t.Errorf("rotate name = %q, want prod-eu", name)
			}
			return "tok-fresh", nil
		},
	}
	h := newClusterRouter(svc)

	rec := do(t, h, http.MethodPost, "/api/clusters/prod-eu/rotate-ingest-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ingest_token":"tok-fresh"`) {
		t.Fatalf("rotate response = %s, want the fresh token", rec.Body.String())
	}

	svc.rotateIngestToken = func(string) (string, error) {
		return "", ports.ErrNotFound
	}
	if rec := do(t, h, http.MethodPost, "/api/clusters/ghost/rotate-ingest-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("rotate missing = %d, want 404", rec.Code)
	}
}

func TestCreateCluster_Operator_UsesSecretRef(t *testing.T) {
	t.Parallel()
	var gotSecretRef string
	svc := &stubClusterService{
		registerOperator: func(e clusterregistry.Cluster) (clusterregistry.Cluster, error) {
			gotSecretRef = e.SecretRef
			e.Origin = clusterregistry.OriginOperator
			return e, nil
		},
	}
	h := newClusterRouter(svc)

	rec := doForm(t, h, http.MethodPost, "/api/clusters", url.Values{
		"name": {"prod-eu"}, "ingest_url": {"http://ingest"}, "sidecar_image": {"img"},
		"namespace": {"honryu"}, "secret_ref": {"existing-secret"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create operator = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if gotSecretRef != "existing-secret" {
		t.Fatalf("secret_ref not forwarded: %q", gotSecretRef)
	}
	// Phase 109: a registration without geo/zone stays quiet on the wire.
	if strings.Contains(rec.Body.String(), `"geo"`) || strings.Contains(rec.Body.String(), `"zone"`) {
		t.Fatalf("unlabeled cluster must omit geo/zone: %s", rec.Body.String())
	}
}

// Phase 109: geo/zone are optional display labels. Registration forwards
// them onto the entry and echoes them back in the response; the BYOC path
// inherits the same wiring (the entry is built before the branch).
func TestCreateCluster_Labels(t *testing.T) {
	t.Parallel()
	var gotEntry clusterregistry.Cluster
	svc := &stubClusterService{
		registerOperator: func(e clusterregistry.Cluster) (clusterregistry.Cluster, error) {
			gotEntry = e
			e.Origin = clusterregistry.OriginOperator
			return e, nil
		},
	}
	h := newClusterRouter(svc)

	rec := doForm(t, h, http.MethodPost, "/api/clusters", url.Values{
		"name": {"prod-eu"}, "ingest_url": {"http://ingest"}, "sidecar_image": {"img"},
		"namespace": {"honryu"}, "secret_ref": {"existing-secret"},
		"geo": {"eu"}, "zone": {"eu-central-1"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if gotEntry.Geo != "eu" || gotEntry.Zone != "eu-central-1" {
		t.Fatalf("entry labels = %q/%q, want eu/eu-central-1", gotEntry.Geo, gotEntry.Zone)
	}
	if !strings.Contains(rec.Body.String(), `"geo":"eu"`) || !strings.Contains(rec.Body.String(), `"zone":"eu-central-1"`) {
		t.Fatalf("response missing labels: %s", rec.Body.String())
	}
}

func TestCreateCluster_ErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"validation", clusterregistry.ErrNameRequired, http.StatusBadRequest},
		{"invalid kubeconfig", clusterapp.ErrKubeconfigInvalid, http.StatusBadRequest},
		{"duplicate", ports.ErrClusterExists, http.StatusConflict},
		{"probe failure", &ports.ProbeError{Kind: ports.ProbeUnderPrivileged, Message: "missing configmaps"}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &stubClusterService{
				registerBYOC: func(clusterregistry.Cluster, []byte) (clusterapp.RegisterResult, error) {
					return clusterapp.RegisterResult{}, tc.err
				},
			}
			h := newClusterRouter(svc)
			rec := doForm(t, h, http.MethodPost, "/api/clusters", url.Values{
				"name": {"prod-eu"}, "ingest_url": {"http://ingest"}, "sidecar_image": {"img"},
				"namespace": {"honryu"}, "kubeconfig": {"kc"},
			})
			if rec.Code != tc.want {
				t.Fatalf("create (%s) = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestGetCluster_NotFound(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{get: func(string) (clusterregistry.Cluster, error) {
		return clusterregistry.Cluster{}, ports.ErrNotFound
	}}
	h := newClusterRouter(svc)
	rec := doForm(t, h, http.MethodGet, "/api/clusters/ghost", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get(ghost) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestListClusters(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{list: func() ([]clusterregistry.Cluster, error) {
		return []clusterregistry.Cluster{{Name: "prod-eu", Origin: clusterregistry.OriginOperator}}, nil
	}}
	h := newClusterRouter(svc)
	rec := doForm(t, h, http.MethodGet, "/api/clusters", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"prod-eu"`) {
		t.Fatalf("list body missing cluster: %s", rec.Body.String())
	}
}

func TestDeleteCluster_GuardConflict(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{del: func(string) error { return clusterapp.ErrClusterInUse }}
	h := newClusterRouter(svc)
	rec := doForm(t, h, http.MethodDelete, "/api/clusters/prod-eu", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete (in use) = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteCluster_HappyPath(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{del: func(string) error { return nil }}
	h := newClusterRouter(svc)
	rec := doForm(t, h, http.MethodDelete, "/api/clusters/prod-eu", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
}

func TestClusters_NotConfiguredReturns404(t *testing.T) {
	t.Parallel()
	// No Clusters dep wired.
	h := httpapi.NewRouter(httpapi.Deps{DefaultOwners: []string{"honryu"}})
	rec := doForm(t, h, http.MethodGet, "/api/clusters", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("list (not configured) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// updateCluster replaces a cluster's mutable deploy settings and answers with
// the ordinary cluster shape (openapi PUT /api/clusters/{name}). Phase 109:
// geo/zone ride the same PUT with full-replace semantics -- present values
// relabel, absent values clear.
func TestUpdateCluster_HappyPath(t *testing.T) {
	t.Parallel()
	var gotName, gotIngest, gotSidecar, gotNS, gotGeo, gotZone string
	svc := &stubClusterService{update: func(name, ingest, sidecar, ns, geo, zone string) (clusterregistry.Cluster, error) {
		gotName, gotIngest, gotSidecar, gotNS, gotGeo, gotZone = name, ingest, sidecar, ns, geo, zone
		return clusterregistry.Cluster{
			Name:         name,
			APIURL:       "https://api:6443",
			IngestURL:    ingest,
			SidecarImage: sidecar,
			Namespace:    ns,
			SecretRef:    "prod-eu-secret",
			Origin:       clusterregistry.OriginOperator,
			Geo:          geo,
			Zone:         zone,
			CreatedTime:  time.Unix(1700000000, 0).UTC(),
		}, nil
	}}
	h := newClusterRouter(svc)

	rec := doForm(t, h, http.MethodPut, "/api/clusters/prod-eu", url.Values{
		"ingest_url": {"http://ingest2"}, "sidecar_image": {"img:2"}, "namespace": {"honryu-system"},
		"geo": {"eu"}, "zone": {"eu-central-1"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if gotName != "prod-eu" || gotIngest != "http://ingest2" || gotSidecar != "img:2" || gotNS != "honryu-system" ||
		gotGeo != "eu" || gotZone != "eu-central-1" {
		t.Fatalf("update args = %q/%q/%q/%q/%q/%q, want prod-eu/http://ingest2/img:2/honryu-system/eu/eu-central-1",
			gotName, gotIngest, gotSidecar, gotNS, gotGeo, gotZone)
	}
	var body struct {
		Name         string    `json:"name"`
		APIURL       string    `json:"api_url"`
		IngestURL    string    `json:"ingest_url"`
		SidecarImage string    `json:"sidecar_image"`
		Namespace    string    `json:"namespace"`
		SecretRef    string    `json:"secret_ref"`
		Origin       string    `json:"origin"`
		Geo          string    `json:"geo"`
		Zone         string    `json:"zone"`
		CreatedTime  time.Time `json:"created_time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode update response: %v (%s)", err, rec.Body.String())
	}
	if body.Name != "prod-eu" || body.APIURL != "https://api:6443" || body.IngestURL != "http://ingest2" ||
		body.SidecarImage != "img:2" || body.Namespace != "honryu-system" ||
		body.SecretRef != "prod-eu-secret" || body.Origin != "operator" || body.CreatedTime.IsZero() {
		t.Fatalf("update response = %+v, want the full cluster shape", body)
	}
	if body.Geo != "eu" || body.Zone != "eu-central-1" {
		t.Fatalf("update response labels = %q/%q, want eu/eu-central-1", body.Geo, body.Zone)
	}
}

// A PUT that omits geo/zone clears them: full-replace semantics, the same
// rule the handler applies to every other mutable field.
func TestUpdateCluster_PutClearsLabels(t *testing.T) {
	t.Parallel()
	var gotGeo, gotZone string
	svc := &stubClusterService{update: func(_, _, _, _, geo, zone string) (clusterregistry.Cluster, error) {
		gotGeo, gotZone = geo, zone
		return clusterregistry.Cluster{Name: "prod-eu", Origin: clusterregistry.OriginOperator, CreatedTime: time.Unix(1700000000, 0).UTC()}, nil
	}}
	h := newClusterRouter(svc)

	rec := doForm(t, h, http.MethodPut, "/api/clusters/prod-eu", url.Values{
		"ingest_url": {"http://ingest2"}, "sidecar_image": {"img:2"}, "namespace": {"honryu-system"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if gotGeo != "" || gotZone != "" {
		t.Fatalf("update args labels = %q/%q, want empty (PUT clears)", gotGeo, gotZone)
	}
	if strings.Contains(rec.Body.String(), `"geo"`) || strings.Contains(rec.Body.String(), `"zone"`) {
		t.Fatalf("cleared labels must be omitted from the wire: %s", rec.Body.String())
	}
}

func TestUpdateCluster_ErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"missing cluster", ports.ErrNotFound, http.StatusNotFound},
		{"validation", clusterregistry.ErrIngestURLRequired, http.StatusBadRequest},
		{"label too long", clusterregistry.ErrLabelTooLong, http.StatusBadRequest},
		{"in use", clusterapp.ErrClusterInUse, http.StatusConflict},
		{"internal", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &stubClusterService{update: func(string, string, string, string, string, string) (clusterregistry.Cluster, error) {
				return clusterregistry.Cluster{}, tc.err
			}}
			h := newClusterRouter(svc)
			rec := doForm(t, h, http.MethodPut, "/api/clusters/prod-eu", url.Values{"ingest_url": {"http://ingest"}})
			if rec.Code != tc.want {
				t.Fatalf("update (%s) = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestUpdateCluster_MalformedForm(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{update: func(string, string, string, string, string, string) (clusterregistry.Cluster, error) {
		t.Fatal("update must not run for an unparseable form")
		return clusterregistry.Cluster{}, nil
	}}
	h := newClusterRouter(svc)
	rec := doRaw(t, h, http.MethodPut, "/api/clusters/prod-eu", "%zz=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update (malformed form) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateCluster_MalformedForm(t *testing.T) {
	t.Parallel()
	h := newClusterRouter(&stubClusterService{})
	rec := doRaw(t, h, http.MethodPost, "/api/clusters", "%zz=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create (malformed form) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestListClusters_Error(t *testing.T) {
	t.Parallel()
	svc := &stubClusterService{list: func() ([]clusterregistry.Cluster, error) {
		return nil, errors.New("boom")
	}}
	h := newClusterRouter(svc)
	if rec := doForm(t, h, http.MethodGet, "/api/clusters", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("list (error) = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

// Without the cluster registry every cluster route, mutation and read alike,
// is a 404 -- the same optional-dependency contract the tenants and campaigns
// gates enforce.
func TestClusterRoutes_NotConfigured(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{DefaultOwners: []string{"honryu"}})
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/clusters"},
		{http.MethodPost, "/api/clusters/prod-eu/rotate-ingest-token"},
		{http.MethodGet, "/api/clusters/prod-eu"},
		{http.MethodPut, "/api/clusters/prod-eu"},
		{http.MethodDelete, "/api/clusters/prod-eu"},
	}
	for _, tc := range cases {
		if rec := doForm(t, h, tc.method, tc.path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s (not configured) = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

// Cluster-registry management is platform-admin gated: a tenant admin, however
// privileged within its tenant, is forbidden; a service-provider admin is not.
func TestClusters_RBAC_PlatformAdminGate(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("val-tok", account.Account{Subject: "val"})
	assignRole(t, f, acme, "val", "tenant_admin")

	forbidden := f.req(t, http.MethodGet, "/api/clusters", "val-tok", nil)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("list (tenant admin) = %d, want 403 (%s)", forbidden.Code, forbidden.Body.String())
	}
	forbiddenPut := f.req(t, http.MethodPut, "/api/clusters/prod-eu", "val-tok", url.Values{"namespace": {"x"}})
	if forbiddenPut.Code != http.StatusForbidden {
		t.Fatalf("update (tenant admin) = %d, want 403 (%s)", forbiddenPut.Code, forbiddenPut.Body.String())
	}
	allowed := f.req(t, http.MethodGet, "/api/clusters", "admin-tok", nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("list (SP admin) = %d, want 200 (%s)", allowed.Code, allowed.Body.String())
	}
}
