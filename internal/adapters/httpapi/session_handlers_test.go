package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/auth/session"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/authapp"
)

// legacyMeRouter wires /api/me exactly as a legacy (RBAC-off) deployment
// does: the demo provider is both the auth provider and the session issuer,
// with RBAC disabled so the middleware passes through and the handler asks
// the provider itself -- the branch TestRBAC_DemoSessionLifecycle never
// reaches, because there the middleware answers 401 first.
func legacyMeRouter(t *testing.T) (http.Handler, *session.Provider) {
	t.Helper()
	prov, err := session.New([]byte("legacy-test-signing-key"), []session.Profile{{
		ID: "dave", Name: "Dave", Email: "dave@honryu.example",
		Global:  []string{"auditor"},
		Tenants: map[int64][]string{7: {"viewer"}},
	}})
	if err != nil {
		t.Fatalf("session provider: %v", err)
	}
	return httpapi.NewRouter(httpapi.Deps{
		Auth:     authapp.NewService(prov, nil, false),
		Sessions: prov,
	}), prov
}

// meWithCookie sends GET /api/me carrying cookie (omitted when empty).
func meWithCookie(t *testing.T, h http.Handler, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMeLegacyCookieVerdicts drives the handler's own provider call through
// every rejection the cookie can earn -- missing, malformed, tampered, and
// expired (the clock is advanced past TTL on the provider itself, the same
// SetNow hook the session package's own tests use) -- plus the valid-cookie
// 200. The RBAC-mode 401s are TestRBAC_DemoSessionLifecycle's contract.
func TestMeLegacyCookieVerdicts(t *testing.T) {
	t.Parallel()
	h, prov := legacyMeRouter(t)
	valid, err := prov.Issue("dave")
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	cases := []struct {
		name    string
		cookie  string
		expired bool
		want    int
	}{
		{name: "no cookie", want: http.StatusUnauthorized},
		{name: "not a session value", cookie: "not-a-session", want: http.StatusUnauthorized},
		// Tampering with the payload half breaks the HMAC before anything
		// decodes.
		{name: "tampered payload", cookie: "e" + valid, want: http.StatusUnauthorized},
		{name: "expired cookie", expired: true, want: http.StatusUnauthorized},
		{name: "valid cookie", cookie: valid, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The expired case shifts the provider's clock, so subtests run
			// sequentially over the shared provider.
			cookie := tc.cookie
			if tc.expired {
				base := time.Now()
				prov.SetNow(func() time.Time { return base })
				cookie, err = prov.Issue("dave")
				if err != nil {
					t.Fatalf("issue session: %v", err)
				}
				prov.SetNow(func() time.Time { return base.Add(session.TTL + time.Minute) })
			} else {
				prov.SetNow(time.Now)
			}
			rec := meWithCookie(t, h, cookie)
			if rec.Code != tc.want {
				t.Fatalf("GET /api/me = %d (%s), want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

// TestMeLegacyResponseShape pins the legacy-mode happy path's wire shape:
// the account's own fields verbatim, the permission map a legacy caller
// reads (RBAC off means the full catalog), and demo=true because the
// session issuer is wired. The RBAC-mode shape is
// TestRBAC_DemoSessionLifecycle's contract.
func TestMeLegacyResponseShape(t *testing.T) {
	t.Parallel()
	h, prov := legacyMeRouter(t)
	value, err := prov.Issue("dave")
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	rec := meWithCookie(t, h, value)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/me = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var me struct {
		Subject     string              `json:"subject"`
		Name        string              `json:"name"`
		Email       string              `json:"email"`
		GlobalRoles []string            `json:"global_roles"`
		Tenants     map[int64][]string  `json:"tenants"`
		Permissions map[string][]string `json:"permissions"`
		Demo        bool                `json:"demo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /api/me: %v (%s)", err, rec.Body.String())
	}
	if me.Subject != "dave" || me.Name != "Dave" || me.Email != "dave@honryu.example" {
		t.Errorf("/api/me identity = %q/%q/%q, want dave/Dave/dave@honryu.example", me.Subject, me.Name, me.Email)
	}
	if len(me.GlobalRoles) != 1 || me.GlobalRoles[0] != "auditor" {
		t.Errorf("/api/me global_roles = %v, want [auditor]", me.GlobalRoles)
	}
	if roles := me.Tenants[7]; len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("/api/me tenants[7] = %v, want [viewer]", roles)
	}
	if len(me.Permissions) == 0 {
		t.Errorf("/api/me permissions = %v, want the legacy full catalog", me.Permissions)
	}
	if !me.Demo {
		t.Errorf("/api/me demo = false with the session issuer wired, want true")
	}
}

// TestMeAuthNotConfigured pins the optional-service answer: a deployment
// that wires no auth provider has no /api/me to serve, and the handler says
// so with the not-configured 404 rather than a 500 on the nil dep.
func TestMeAuthNotConfigured(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{})
	rec := meWithCookie(t, h, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/me = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "auth not configured") {
		t.Errorf("GET /api/me body = %s, want the not-configured reason", rec.Body.String())
	}
}
