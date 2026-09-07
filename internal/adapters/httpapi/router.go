// Package httpapi is the inbound HTTP adapter: it maps REST requests onto the
// application services. Handlers are thin — parse/validate the request, call a
// use-case, and serialize the result. All dependencies are injected via Deps,
// so the router is fully exercised in unit tests with in-memory fakes.
package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/heridotlife/honryu/internal/adapters/auth/session"
	"github.com/heridotlife/honryu/internal/app/adminapp"
	"github.com/heridotlife/honryu/internal/app/authapp"
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/scheduleapp"
	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/app/usageapp"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/ports"
)

// Deps are the collaborators the HTTP layer needs.
type Deps struct {
	Projects   *projectapp.Service
	Scenarios  *scenarioapp.Service
	Executions *executionapp.Service
	Lifecycle  *lifecycleapp.Service
	// Schedules administers time-triggered executions. Optional; nil disables
	// the /api/executions/{execution_id}/schedules endpoints.
	Schedules *scheduleapp.Service
	// Campaigns administers PM-owned readiness events. Optional; nil disables
	// the /api/tenants/{tenant_id}/campaigns, /api/campaigns/{campaign_id},
	// and /api/campaigns/{campaign_id}/verdict endpoints.
	Campaigns *campaignapp.Service
	// Calibrations administers engine-capacity searches and the fan-out
	// calculator. Optional; nil disables the /api/calibrations,
	// /api/executions/{execution_id}/calibration/trigger, and
	// /api/scenarios/{scenario_id}/capacity-profile[/fanout] endpoints.
	Calibrations *calibrationapp.Service
	Usage        *usageapp.Service
	// Metrics receives pushed measurements from engine pods.
	Metrics *metricsapp.Service
	// Reports serves a run's stored report, the durable record of what it
	// produced.
	Reports ports.ReportStore
	// Series reads a run's per-second measurements for the series endpoint.
	// Optional; nil disables GET /api/runs/{run_id}/series (404), which serves
	// report-store-only deployments that never absorbed intervals.
	Series ports.IntervalRepository
	// Reservations backs the reservation calendar (GET
	// /api/tenants/{tenant_id}/reservations). Required alongside Tenants for
	// that endpoint; nil disables it (tenantAdminGate 404s first anyway).
	Reservations ports.ReservationRepository
	// IngestToken authenticates engine pods. Empty rejects every push, so a
	// deployment that has not configured one is closed rather than open.
	// This is the operator's plane-wide credential; registered clusters
	// (BYOC) instead authenticate per cluster below.
	IngestToken string
	// IngestTokens resolves a presented cluster ingest token's hash to its
	// cluster (nil disables the per-cluster path: only the global token
	// authenticates, exactly as before Phase 12). The mysql repository
	// implements it via ClusterByIngestTokenHash.
	IngestTokens IngestTokenResolver
	// ExecutionCluster loads an execution's cluster name for ingest scoping
	// (nil behaves like IngestTokens nil: the per-cluster path is off).
	ExecutionCluster ExecutionClusterLoader
	Admin            *adminapp.Service
	Events           ports.EventBus
	Store            ports.ObjectStore
	// Auth authenticates requests and authorizes actions. When nil or disabled,
	// the legacy no-auth owner path applies (DefaultOwners).
	Auth *authapp.Service
	// Quota reads the reservation ledger's aggregate engine capacity for
	// cluster responses (phase 25). Optional; nil keeps /api/clusters bodies
	// byte-identical to before (no engines_used/engines_ceiling fields).
	Quota *quotaapp.Service
	// Tenants administers tenants and role grants. Required for the /api/tenants
	// endpoints; nil disables them.
	Tenants *tenantapp.Service
	// Clusters administers the cluster registry (platform-admin gated).
	// Optional; nil disables the /api/clusters endpoints.
	Clusters ClusterService
	// Audit records administrative actions. Optional; nil disables auditing.
	Audit ports.AuditLog
	// DefaultOwners is the owner set used when RBAC is disabled (no-auth mode).
	DefaultOwners []string
	// Sessions mints and lists demo persona sessions (AUTH_MODE=demo).
	// Optional; nil disables the /api/session endpoints (404), which is what
	// every non-demo deployment wants.
	Sessions SessionService
	// TriggerReadyPoll is how often POST /trigger retries while a just-deployed
	// execution's engine pods are still starting up. Zero means the default
	// (2s, matching calibrationapp's own readiness loop).
	TriggerReadyPoll time.Duration
	// TriggerReadyTimeout bounds how long POST /trigger waits for those pods
	// before returning the last conflict. Zero means the default (2m). The
	// wait is per-request: cancelling the request context cancels the wait.
	TriggerReadyTimeout time.Duration
	// StaticAssets serves the SPA build (web/dist, unwrapped from web.Dist's
	// "dist/" prefix by the caller) for any unmatched non-/api/ path.
	// Optional; nil disables static serving (e.g. in tests that only exercise
	// the JSON API).
	StaticAssets fs.FS
}

// SessionService is the demo-session surface the /api/session endpoints
// need: mint a signed cookie for a named persona, and list the personas the
// picker may offer. Implemented by the session provider
// (adapters/auth/session.Provider).
type SessionService interface {
	// Issue signs a session cookie value for the named persona.
	Issue(profileID string) (string, error)
	// Profiles returns the configured personas in configuration order.
	Profiles() []session.Profile
}

// ingestPath is the engine-pod push endpoint. It authenticates with its own
// credential rather than the user provider, so the router exempts it from user
// authentication.
const ingestPath = "/api/ingest"

// sessionPath and sessionProfilesPath are how a caller authenticates in demo
// mode: the middleware cannot demand authentication for the endpoints whose
// whole job is to establish it. Expiry (DELETE) rides along: clearing a
// session never needs to prove one.
const (
	sessionPath         = "/api/session"
	sessionProfilesPath = "/api/session/profiles"
)

// publicAPIPath reports whether an API path is exempt from the user auth
// middleware: it either carries its own credential (ingest) or is itself the
// authentication (demo session endpoints).
func publicAPIPath(path string) bool {
	return path == ingestPath || path == sessionPath || path == sessionProfilesPath
}

// Route is one registered endpoint. The route table is the single source of
// truth for the API surface: NewRouter registers from it, and a test asserts
// that api/openapi.yaml documents exactly these routes.
type Route struct {
	Method  string
	Pattern string
	// Group is the section the route belongs to, used for OpenAPI tags.
	Group string

	handler func(*handlers) http.Handler
}

func hf(f func(*handlers) http.HandlerFunc) func(*handlers) http.Handler {
	return func(h *handlers) http.Handler { return f(h) }
}

var routes = []Route{
	{"GET", "/healthz", "health", hf(func(h *handlers) http.HandlerFunc { return h.health })},
	{"GET", "/metrics", "health", func(*handlers) http.Handler { return promhttp.Handler() }},

	{"GET", "/api/session/profiles", "session", hf(func(h *handlers) http.HandlerFunc { return h.listSessionProfiles })},
	{"POST", "/api/session", "session", hf(func(h *handlers) http.HandlerFunc { return h.createSession })},
	{"DELETE", "/api/session", "session", hf(func(h *handlers) http.HandlerFunc { return h.deleteSession })},
	{"GET", "/api/me", "session", hf(func(h *handlers) http.HandlerFunc { return h.me })},

	{"GET", "/api/projects", "projects", hf(func(h *handlers) http.HandlerFunc { return h.listProjects })},
	{"POST", "/api/projects", "projects", hf(func(h *handlers) http.HandlerFunc { return h.createProject })},
	{"GET", "/api/projects/{project_id}", "projects", hf(func(h *handlers) http.HandlerFunc { return h.getProject })},
	{"DELETE", "/api/projects/{project_id}", "projects", hf(func(h *handlers) http.HandlerFunc { return h.deleteProject })},

	{"POST", "/api/scenarios", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.createScenario })},
	{"POST", "/api/scenarios/import", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.importScenario })},
	{"GET", "/api/scenarios/{scenario_id}", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.getScenario })},
	{"DELETE", "/api/scenarios/{scenario_id}", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.deleteScenario })},
	{"GET", "/api/scenarios/{scenario_id}/files", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.listScenarioFiles })},
	{"PUT", "/api/scenarios/{scenario_id}/files", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.uploadScenarioFile })},
	{"DELETE", "/api/scenarios/{scenario_id}/files", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.deleteScenarioFile })},
	{"GET", "/api/scenarios/{scenario_id}/requests", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.getScenarioRequests })},
	{"POST", "/api/scenarios/{scenario_id}/requests/validate", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.validateScenarioRequests })},
	{"PUT", "/api/scenarios/{scenario_id}/requests", "scenarios", hf(func(h *handlers) http.HandlerFunc { return h.setScenarioRequests })},

	{"POST", "/api/executions", "executions", hf(func(h *handlers) http.HandlerFunc { return h.createExecution })},
	{"GET", "/api/executions", "executions", hf(func(h *handlers) http.HandlerFunc { return h.listExecutions })},
	{"GET", "/api/executions/{execution_id}", "executions", hf(func(h *handlers) http.HandlerFunc { return h.getExecution })},
	{"DELETE", "/api/executions/{execution_id}", "executions", hf(func(h *handlers) http.HandlerFunc { return h.deleteExecution })},
	{"GET", "/api/executions/{execution_id}/files", "executions", hf(func(h *handlers) http.HandlerFunc { return h.listExecutionFiles })},
	{"PUT", "/api/executions/{execution_id}/files", "executions", hf(func(h *handlers) http.HandlerFunc { return h.uploadExecutionFile })},
	{"DELETE", "/api/executions/{execution_id}/files", "executions", hf(func(h *handlers) http.HandlerFunc { return h.deleteExecutionFile })},
	{"PUT", "/api/executions/{execution_id}/config", "executions", hf(func(h *handlers) http.HandlerFunc { return h.uploadExecutionConfig })},
	{"GET", "/api/executions/{execution_id}/config", "executions", hf(func(h *handlers) http.HandlerFunc { return h.getExecutionConfig })},

	{"POST", "/api/executions/{execution_id}/deploy", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.deployExecution })},
	{"POST", "/api/executions/{execution_id}/trigger", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.triggerExecution })},
	{"POST", "/api/executions/{execution_id}/stop", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.stopExecution })},
	{"POST", "/api/executions/{execution_id}/purge", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.purgeExecution })},
	{"GET", "/api/executions/{execution_id}/status", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.executionStatus })},
	{"GET", "/api/executions/{execution_id}/engines", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.executionEngines })},
	{"GET", "/api/executions/{execution_id}/scenarios/{scenario_id}/logs", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.scenarioPodLog })},
	{"GET", "/api/executions/{execution_id}/stream", "lifecycle", hf(func(h *handlers) http.HandlerFunc { return h.streamExecution })},

	{"POST", "/api/executions/{execution_id}/schedules", "schedules", hf(func(h *handlers) http.HandlerFunc { return h.createSchedule })},
	{"GET", "/api/executions/{execution_id}/schedules", "schedules", hf(func(h *handlers) http.HandlerFunc { return h.listSchedules })},
	{"DELETE", "/api/executions/{execution_id}/schedules/{schedule_id}", "schedules", hf(func(h *handlers) http.HandlerFunc { return h.deleteSchedule })},

	{"GET", "/api/executions/{execution_id}/reports", "reports", hf(func(h *handlers) http.HandlerFunc { return h.executionReports })},
	{"GET", "/api/executions/{execution_id}/trend", "reports", hf(func(h *handlers) http.HandlerFunc { return h.executionTrend })},
	{"GET", "/api/executions/{execution_id}/error-signatures", "reports", hf(func(h *handlers) http.HandlerFunc { return h.executionErrorSignatureHistory })},
	{"GET", "/api/runs/{run_id}/report", "reports", hf(func(h *handlers) http.HandlerFunc { return h.runReport })},
	{"GET", "/api/runs/{run_id}/series", "reports", hf(func(h *handlers) http.HandlerFunc { return h.runSeries })},
	{"GET", "/api/runs/{run_id}/export", "reports", hf(func(h *handlers) http.HandlerFunc { return h.runExport })},
	{"GET", "/api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/log", "reports", hf(func(h *handlers) http.HandlerFunc { return h.runShardLog })},
	{"GET", "/api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/config", "reports", hf(func(h *handlers) http.HandlerFunc { return h.runShardConfig })},

	{"GET", "/api/usage/history", "usage", hf(func(h *handlers) http.HandlerFunc { return h.usageHistory })},
	{"GET", "/api/usage/summary", "usage", hf(func(h *handlers) http.HandlerFunc { return h.usageSummary })},

	{"GET", "/api/admin/executions", "admin", hf(func(h *handlers) http.HandlerFunc { return h.adminExecutions })},
	{"GET", "/api/admin/nodes", "admin", hf(func(h *handlers) http.HandlerFunc { return h.adminNodes })},
	{"POST", "/api/admin/abort", "admin", hf(func(h *handlers) http.HandlerFunc { return h.abortExecutions })},

	{"POST", "/api/tenants", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.createTenant })},
	{"GET", "/api/tenants", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.listTenants })},
	{"GET", "/api/tenants/{tenant_id}", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.getTenant })},
	{"PATCH", "/api/tenants/{tenant_id}", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.setTenantStatus })},
	{"PUT", "/api/tenants/{tenant_id}/quota", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.setTenantQuota })},
	{"GET", "/api/tenants/{tenant_id}/quota", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.getTenantQuota })},
	{"GET", "/api/tenants/{tenant_id}/reservations", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.tenantReservations })},
	{"POST", "/api/tenants/{tenant_id}/roles", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.assignTenantRole })},
	{"DELETE", "/api/tenants/{tenant_id}/roles", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.revokeTenantRole })},
	{"POST", "/api/roles", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.assignGlobalRole })},
	{"DELETE", "/api/roles", "tenants", hf(func(h *handlers) http.HandlerFunc { return h.revokeGlobalRole })},

	{"POST", "/api/clusters", "clusters", hf(func(h *handlers) http.HandlerFunc { return h.createCluster })},
	{"GET", "/api/clusters", "clusters", hf(func(h *handlers) http.HandlerFunc { return h.listClusters })},
	{"POST", "/api/clusters/{name}/rotate-ingest-token", "clusters", hf(func(h *handlers) http.HandlerFunc { return h.rotateIngestToken })},
	{"GET", "/api/clusters/{name}", "clusters", hf(func(h *handlers) http.HandlerFunc { return h.getCluster })},
	{"PUT", "/api/clusters/{name}", "clusters", hf(func(h *handlers) http.HandlerFunc { return h.updateCluster })},
	{"DELETE", "/api/clusters/{name}", "clusters", hf(func(h *handlers) http.HandlerFunc { return h.deleteCluster })},

	{"POST", "/api/tenants/{tenant_id}/campaigns", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.createCampaign })},
	{"GET", "/api/tenants/{tenant_id}/campaigns", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.listCampaigns })},
	{"GET", "/api/campaigns", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.listAllCampaigns })},
	{"GET", "/api/campaigns/{campaign_id}", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.getCampaign })},
	{"PUT", "/api/campaigns/{campaign_id}", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.updateCampaign })},
	{"POST", "/api/campaigns/{campaign_id}/abort", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.abortCampaign })},
	{"GET", "/api/campaigns/{campaign_id}/verdict", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.getCampaignVerdict })},
	{"GET", "/api/campaigns/{campaign_id}/comparison", "campaigns", hf(func(h *handlers) http.HandlerFunc { return h.getCampaignComparison })},

	{"POST", "/api/calibrations", "calibration", hf(func(h *handlers) http.HandlerFunc { return h.createCalibration })},
	{"POST", "/api/executions/{execution_id}/calibration/trigger", "calibration", hf(func(h *handlers) http.HandlerFunc { return h.triggerCalibration })},
	{"GET", "/api/calibrations/{job_id}", "calibration", hf(func(h *handlers) http.HandlerFunc { return h.getCalibrationJob })},
	{"GET", "/api/scenarios/{scenario_id}/capacity-profile", "calibration", hf(func(h *handlers) http.HandlerFunc { return h.getCapacityProfile })},
	{"GET", "/api/scenarios/{scenario_id}/capacity-profile/fanout", "calibration", hf(func(h *handlers) http.HandlerFunc { return h.fanOutCapacity })},

	{"GET", "/api/files/{kind}/{id}/{name}", "files", hf(func(h *handlers) http.HandlerFunc { return h.downloadFile })},

	{"POST", ingestPath, "ingest", hf(func(h *handlers) http.HandlerFunc { return h.ingest })},
}

// Routes returns the registered API surface. Handlers are not exposed; callers
// get the method, pattern, and group only.
func Routes() []Route {
	out := make([]Route, len(routes))
	for i, r := range routes {
		out[i] = Route{Method: r.Method, Pattern: r.Pattern, Group: r.Group}
	}
	return out
}

// NewRouter builds the HTTP handler for the API server.
func NewRouter(d Deps) http.Handler {
	h := &handlers{deps: d, sleep: time.Sleep}

	mux := http.NewServeMux()
	for _, r := range routes {
		mux.Handle(r.Method+" "+r.Pattern, r.handler(h))
	}
	if d.StaticAssets != nil {
		// A registered "/" pattern is ServeMux's catch-all: every /api/...
		// pattern above is more specific and always wins for a matching
		// request, so this only ever runs for what none of them claimed.
		mux.Handle("/", newStaticHandler(d.StaticAssets))
	}

	return h.authenticate(mux)
}

// authenticate is the auth middleware. When RBAC is enabled, it authenticates
// every /api/ request (rejecting unauthenticated callers with 401) and stashes
// the account on the request context for downstream authorization. When RBAC is
// disabled it is a pass-through and the legacy owner checks apply.
// Endpoints that carry their own credential or are themselves the
// authentication (ingest, demo session) are exempt: publicAPIPath.
func (h *handlers) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Engine pods carry a deployment credential, not a user account, and
		// authenticate in the ingest handler itself. Sending them through the
		// user provider would reject every push the moment RBAC was enabled.
		if publicAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if h.rbacEnabled() && strings.HasPrefix(r.URL.Path, "/api/") {
			acct, err := h.deps.Auth.Authenticate(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthenticated")
				return
			}
			r = r.WithContext(withAccount(r.Context(), acct))
		}
		next.ServeHTTP(w, r)
	})
}

type handlers struct {
	deps  Deps
	sleep func(time.Duration)
}

// IngestTokenResolver is the per-cluster ingest-token lookup: SHA-256 of the
// presented token to the cluster whose fleet holds it.
type IngestTokenResolver interface {
	ClusterByIngestTokenHash(ctx context.Context, hash string) (clusterregistry.Cluster, error)
}

// ExecutionClusterLoader names the cluster an execution is routed to -- the
// one field ingest scoping needs from the execution.
type ExecutionClusterLoader interface {
	GetExecution(ctx context.Context, executionID int64) (execution.Execution, error)
}

// defaultTriggerReadyPoll and defaultTriggerReadyTimeout mirror
// calibrationapp's own readiness loop constants (triggerReadyPollInterval /
// triggerReadyTimeout, step.go): the same 2s/2m that has bounded the
// scheduler-side retry since Phase 7 now also bounds the HTTP boundary.
const (
	defaultTriggerReadyPoll    = 2 * time.Second
	defaultTriggerReadyTimeout = 2 * time.Minute
)

func (h *handlers) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
