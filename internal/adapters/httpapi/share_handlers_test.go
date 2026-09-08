package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/auth/session"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/authapp"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newShareEnv wires the report store and share store the way cmd/api does,
// in legacy no-auth mode (DefaultOwners admits every caller, like the other
// report handler tests).
func newShareEnv(t *testing.T) (http.Handler, *fake.ReportStore, *fake.ShareStore) {
	t.Helper()
	reports := fake.NewReportStore()
	shares := fake.NewShareStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Reports: reports, Shares: shares, DefaultOwners: []string{"honryu"},
	})
	return h, reports, shares
}

// doBody issues a request with a literal string body, for the JSON bodies
// the share endpoints take.
func doBody(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedRun stores the sample report for run 42 and returns nothing -- every
// share test reads it through the API.
func seedRun(t *testing.T, reports *fake.ReportStore) {
	t.Helper()
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
}

// issueLink POSTs a share link for run 42 with the given JSON body and
// returns the decoded response.
func issueLink(t *testing.T, h http.Handler, body string) shareIssueShape {
	t.Helper()
	rec := doBody(t, h, http.MethodPost, "/api/runs/42/share", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST share = %d (%s)", rec.Code, rec.Body.String())
	}
	var got shareIssueShape
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	return got
}

// shareIssueShape mirrors the POST response the frontend consumes.
type shareIssueShape struct {
	Token     string  `json:"token"`
	URL       string  `json:"url"`
	ExpiresAt *string `json:"expires_at"`
}

// shareLinkShape mirrors one row of the list response.
type shareLinkShape struct {
	Token       string  `json:"token"`
	CreatedBy   string  `json:"created_by"`
	CreatedTime string  `json:"created_time"`
	ExpiresAt   *string `json:"expires_at"`
}

// Issue, then fetch: the public endpoint returns byte-identical content to
// the session'd run report route -- the whole contract of a share link is
// "the same report, no session".
func TestShareHTTP_IssueAndPublicFetch(t *testing.T) {
	t.Parallel()
	h, reports, _ := newShareEnv(t)
	seedRun(t, reports)

	issued := issueLink(t, h, "")
	if len(issued.Token) != 64 {
		t.Errorf("token %q, want 64 hex chars", issued.Token)
	}
	if issued.URL != "/share/"+issued.Token {
		t.Errorf("url = %q, want /share/<token>", issued.URL)
	}
	if issued.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want null for a body-less issue", issued.ExpiresAt)
	}

	shared := do(t, h, http.MethodGet, "/api/share/"+issued.Token)
	if shared.Code != http.StatusOK {
		t.Fatalf("GET shared report = %d (%s)", shared.Code, shared.Body.String())
	}
	direct := do(t, h, http.MethodGet, "/api/runs/42/report")
	if shared.Body.String() != direct.Body.String() {
		t.Errorf("shared payload differs from the run report:\n shared: %s\n direct: %s", shared.Body.String(), direct.Body.String())
	}
}

// Two links for one run: revoking one customer must never break the other.
func TestShareHTTP_MultipleLinksCoexist(t *testing.T) {
	t.Parallel()
	h, reports, _ := newShareEnv(t)
	seedRun(t, reports)

	first := issueLink(t, h, "")
	second := issueLink(t, h, "")
	if first.Token == second.Token {
		t.Fatalf("two issues minted the same token")
	}
	for _, tok := range []string{first.Token, second.Token} {
		if rec := do(t, h, http.MethodGet, "/api/share/"+tok); rec.Code != http.StatusOK {
			t.Errorf("GET /api/share/%s = %d, want 200", tok, rec.Code)
		}
	}
}

// The list shows what is out in the world: both links, issue order, with
// their expiry as null or a timestamp.
func TestShareHTTP_ListAndRevoke(t *testing.T) {
	t.Parallel()
	h, reports, _ := newShareEnv(t)
	seedRun(t, reports)

	never := issueLink(t, h, "")
	expiring := issueLink(t, h, `{"expires_in_hours": 24}`)
	if expiring.ExpiresAt == nil {
		t.Fatalf("expiring issue carries no expires_at")
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/share")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET share list = %d (%s)", rec.Code, rec.Body.String())
	}
	var links []shareLinkShape
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil {
		t.Fatalf("decode share list: %v", err)
	}
	if len(links) != 2 || links[0].Token != never.Token || links[1].Token != expiring.Token {
		t.Fatalf("links = %+v, want both in issue order", links)
	}
	if links[0].ExpiresAt != nil || links[1].ExpiresAt == nil {
		t.Errorf("expiry round trip: %+v", links)
	}
	if links[0].CreatedTime == "" {
		t.Errorf("created_time missing from the list row")
	}

	// Revoke the expiring one; the never-expiring link must keep working.
	if rec := do(t, h, http.MethodDelete, "/api/runs/42/share/"+expiring.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE share = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/share/"+expiring.Token); rec.Code != http.StatusNotFound {
		t.Errorf("revoked link still resolves: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/share/"+never.Token); rec.Code != http.StatusOK {
		t.Errorf("sibling link broken by revoke: %d", rec.Code)
	}
	// Revoking a revoked link is a 404, not a silent success.
	if rec := do(t, h, http.MethodDelete, "/api/runs/42/share/"+expiring.Token); rec.Code != http.StatusNotFound {
		t.Errorf("double revoke = %d, want 404", rec.Code)
	}
}

// Expiry is fetch-time policy: an expired row 404s on the public fetch while
// still being listed for the issuing UI. The cap clamps over-long asks to
// 720h rather than rejecting them.
func TestShareHTTP_ExpiryAndCap(t *testing.T) {
	t.Parallel()
	h, reports, shares := newShareEnv(t)
	seedRun(t, reports)

	capped := issueLink(t, h, `{"expires_in_hours": 100000}`)
	if capped.ExpiresAt == nil {
		t.Fatalf("capped issue carries no expires_at")
	}
	got, err := time.Parse(time.RFC3339, *capped.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q: %v", *capped.ExpiresAt, err)
	}
	remaining := time.Until(got)
	if remaining > 721*time.Hour || remaining < 719*time.Hour {
		t.Errorf("expires_in_hours=100000 minted %v of life, want the 720h cap", remaining)
	}

	// An already-expired link: seeded straight into the store, the only way
	// to test an expiry that already passed without sleeping.
	past := time.Now().Add(-time.Minute).UTC()
	if err := shares.CreateShare(context.Background(), 42, strings.Repeat("d", 64), "dave", &past); err != nil {
		t.Fatalf("seed expired share: %v", err)
	}
	if rec := do(t, h, http.MethodGet, "/api/share/"+strings.Repeat("d", 64)); rec.Code != http.StatusNotFound {
		t.Errorf("expired link fetch = %d, want 404", rec.Code)
	}
	// ... and is still listed, so its revoke button still works.
	rec := do(t, h, http.MethodGet, "/api/runs/42/share")
	var links []shareLinkShape
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil {
		t.Fatalf("decode share list: %v", err)
	}
	if len(links) != 2 {
		t.Errorf("expired link vanished from the list: %d rows", len(links))
	}
}

// Unknown tokens, unknown runs, and bad asks all answer plainly: the public
// fetch's 404 says nothing about which tokens exist.
func TestShareHTTP_NotFoundAndValidation(t *testing.T) {
	t.Parallel()
	h, reports, _ := newShareEnv(t)
	seedRun(t, reports)

	if rec := do(t, h, http.MethodGet, "/api/share/"+strings.Repeat("0", 64)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", rec.Code)
	}
	if rec := doBody(t, h, http.MethodPost, "/api/runs/999/share", ""); rec.Code != http.StatusNotFound {
		t.Errorf("issue for unknown run = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/runs/999/share"); rec.Code != http.StatusNotFound {
		t.Errorf("list for unknown run = %d, want 404", rec.Code)
	}
	if rec := doBody(t, h, http.MethodPost, "/api/runs/42/share", "not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", rec.Code)
	}
	if rec := doBody(t, h, http.MethodPost, "/api/runs/42/share", `{"expires_in_hours": 0}`); rec.Code != http.StatusBadRequest {
		t.Errorf("zero expiry = %d, want 400", rec.Code)
	}
	if rec := doBody(t, h, http.MethodPost, "/api/runs/42/share", `{"expires_in_hours": -5}`); rec.Code != http.StatusBadRequest {
		t.Errorf("negative expiry = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/runs/abc/share"); rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric run id = %d, want 400", rec.Code)
	}
}

// A deployment that wires no share store keeps the endpoints closed: 404,
// the Series dependency's precedent, rather than a half-open surface.
func TestShareHTTP_UnconfiguredStoreIs404(t *testing.T) {
	t.Parallel()
	reports := fake.NewReportStore()
	h := httpapi.NewRouter(httpapi.Deps{Reports: reports, DefaultOwners: []string{"honryu"}})
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/runs/42/share"},
		{http.MethodGet, "/api/runs/42/share"},
		{http.MethodDelete, "/api/runs/42/share/abc"},
		{http.MethodGet, "/api/share/abc"},
	} {
		if rec := do(t, h, c.method, c.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (shares not configured)", c.method, c.path, rec.Code)
		}
	}
}

// The point of the feature, pinned: with RBAC enabled and no session at
// all, the report route rejects while the share fetch serves. publicAPIPath
// is what keeps the public route outside the middleware.
func TestShareHTTP_PublicFetchBypassesAuth(t *testing.T) {
	t.Parallel()
	// No session: the report route is the control group. (session.New needs
	// at least one profile configured; the probe never authenticates as it.)
	prov, err := session.New([]byte("share-test-signing-key"), []session.Profile{
		{ID: "dave", Name: "Dave", Subject: "dave"},
	})
	if err != nil {
		t.Fatalf("session provider: %v", err)
	}
	reports := fake.NewReportStore()
	shares := fake.NewShareStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Auth:    authapp.NewService(prov, fake.NewStore(), true),
		Reports: reports, Shares: shares,
	})
	ctx := context.Background()
	if err := reports.SaveReport(ctx, sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := shares.CreateShare(ctx, 42, strings.Repeat("e", 64), "dave", nil); err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	// No session: the report route is the control group.
	if rec := do(t, h, http.MethodGet, "/api/runs/42/report"); rec.Code != http.StatusUnauthorized {
		t.Errorf("sessionless report fetch = %d, want 401", rec.Code)
	}
	rec := do(t, h, http.MethodGet, "/api/share/"+strings.Repeat("e", 64))
	if rec.Code != http.StatusOK {
		t.Fatalf("sessionless share fetch = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode shared report: %v", err)
	}
	if got.RunID != 42 {
		t.Errorf("shared report run_id = %d, want 42", got.RunID)
	}
	// And the management endpoints stay sessioned even with a valid-looking
	// token: the link's afterlife is the issuer's to manage, not the
	// recipient's.
	if rec := do(t, h, http.MethodGet, "/api/runs/42/share"); rec.Code != http.StatusUnauthorized {
		t.Errorf("sessionless share list = %d, want 401", rec.Code)
	}
}
