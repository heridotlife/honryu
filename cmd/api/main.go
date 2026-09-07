// Command api is the Honryu REST API server.
//
// It wires configuration and adapters into the application services and serves
// HTTP. Wiring lives in run() so it can be unit-tested and so main stays a thin
// shell around it.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	auditmem "github.com/heridotlife/honryu/internal/adapters/audit/memory"
	"github.com/heridotlife/honryu/internal/adapters/auth/noauth"
	"github.com/heridotlife/honryu/internal/adapters/auth/oidc"
	"github.com/heridotlife/honryu/internal/adapters/auth/session"
	eventbus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	promsink "github.com/heridotlife/honryu/internal/adapters/metrics/prometheus"
	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	k8sscheduler "github.com/heridotlife/honryu/internal/adapters/scheduler/k8s"
	"github.com/heridotlife/honryu/internal/adapters/secretbox"
	"github.com/heridotlife/honryu/internal/adapters/storage/local"
	"github.com/heridotlife/honryu/internal/adapters/storage/nexus"
	"github.com/heridotlife/honryu/internal/app/adminapp"
	"github.com/heridotlife/honryu/internal/app/authapp"
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/clusterapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/scheduleapp"
	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/app/usageapp"
	"github.com/heridotlife/honryu/internal/config"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/web"
)

// repository is the full repository surface the API wires into its services.
// Both the in-memory fake and the MySQL adapter satisfy it.
type repository interface {
	projectapp.Repo
	scenarioapp.Repo
	executionapp.Repo
	lifecycleapp.Repo
	calibrationapp.Repo
	ports.UsageRepository
	ports.TenantRepository
	ports.RoleAssignmentRepository
	ports.ReportProgress
	ports.ReportStore
	ports.IntervalRepository
	ports.ReservationRepository
	ports.ScheduleRepository
	ports.CampaignRepository
	ports.ClusterRegistry
	// clusterapp.ActiveRunQuery backs the cluster-registry delete guard.
	clusterapp.ActiveRunQuery
}

func main() {
	if err := realMain(); err != nil {
		slog.Error("api server exited with error", "error", err)
		os.Exit(1)
	}
}

// realMain owns process-scoped setup (signal handling) so main can call
// os.Exit without skipping deferred cleanup.
func realMain() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Getenv)
}

func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	setupLogging(cfg.Log)

	repo, err := newRepository(cfg.DB, cfg.Cluster.Context)
	if err != nil {
		return err
	}
	store, err := newObjectStore(cfg.Storage)
	if err != nil {
		return err
	}

	sched, err := newScheduler(cfg.Cluster, repo)
	if err != nil {
		return err
	}
	clusterSvc, err := newClusterService(cfg.Cluster, repo)
	if err != nil {
		return err
	}
	sink := promsink.New(prometheus.DefaultRegisterer)
	bus := eventbus.New()
	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	// Nothing to resume after a restart: pods push, so a run already under way
	// simply keeps sending to whichever controller is listening.
	usage := usageapp.NewService(repo)
	quota := quotaapp.NewService(repo)
	campaigns := campaignapp.NewService(repo, sched)
	lifecycle := lifecycleapp.NewService(repo, sched, store, cfg.Cluster.ImageFor).
		WithMetrics(collector).WithUsage(usage).WithQuota(quota).WithFreeze(campaigns)
	// Retrofitted after lifecycle exists, since lifecycle itself depends on
	// quota (WithQuota above) -- this is the only order that avoids a
	// circular construction. Lets a manual Trigger reclaim the same tenant's
	// own overrunning capacity, exactly like a scheduled one (cmd/scheduler)
	// can.
	quota.WithStopper(lifecycle)
	schedules := scheduleapp.NewService(repo, quota)
	admin := adminapp.NewService(repo, sched, lifecycle).WithCampaigns(campaigns)
	scenarios := scenarioapp.NewService(repo, store)
	// No WithRunner: cmd/api only serves HTTP (create/trigger/get/profile/
	// fan-out) -- driving a step (AdvanceOne) is cmd/calibrator's and
	// cmd/scheduler's own job, never a synchronous request here.
	calibrations := calibrationapp.NewService(repo).WithFingerprint(scenarios)
	startAutoPurge(ctx, admin, cfg.Cluster)
	startReconcile(ctx, lifecycle, cfg.Cluster)

	authProvider, err := newAuthProvider(ctx, cfg.Auth)
	if err != nil {
		return err
	}
	// The demo session surface rides on the same provider: when it is the
	// session provider, the /api/session endpoints get it directly.
	var sessions httpapi.SessionService
	if sp, ok := authProvider.(*session.Provider); ok {
		sessions = sp
	}
	if cfg.Auth.Demo.Enabled {
		slog.Warn("DEMO SESSION MODE ENABLED: anyone who can reach this deployment can select any persona, including the service provider admin")
	}
	audit := auditmem.New(slog.Default())
	slog.Info("auth configured", "mode", cfg.Auth.Mode, "rbac_enabled", cfg.Auth.EnableRBAC)

	webAssets, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return fmt.Errorf("web assets: %w", err)
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:     projectapp.NewService(repo),
		Scenarios:    scenarios,
		Executions:   executionapp.NewService(repo, store, cfg.Limits.MaxEnginesInExecution),
		Lifecycle:    lifecycle,
		Schedules:    schedules,
		Campaigns:    campaigns,
		Calibrations: calibrations,
		Usage:        usage,
		Metrics:      collector,
		Reports:      repo,
		Series:       repo,
		Reservations: repo,
		IngestToken:  cfg.Cluster.IngestToken,
		// Per-cluster ingest credentials (BYOC): the repository resolves a
		// presented token's hash to its cluster and names an execution's
		// cluster, letting the ingest handler scope cluster tokens.
		IngestTokens:     repo,
		ExecutionCluster: repo,
		Admin:            admin,
		Events:           bus,
		Store:            store,
		Auth:             authapp.NewService(authProvider, repo, cfg.Auth.EnableRBAC),
		Tenants:          tenantapp.NewService(repo, repo, repo),
		Clusters:         clusterSvc,
		Quota:            quota,
		Audit:            audit,
		DefaultOwners:    []string{"honryu"},
		Sessions:         sessions,
		StaticAssets:     webAssets,
		// The trigger endpoint's bounded readiness wait (Phase 11): a
		// client may fire deploy->trigger back-to-back without owning the
		// retry itself.
		TriggerReadyPoll:    cfg.HTTP.TriggerReadyPoll,
		TriggerReadyTimeout: cfg.HTTP.TriggerReadyTimeout,
	})

	srv := &http.Server{
		Addr:           cfg.HTTP.Addr(),
		Handler:        router,
		ReadTimeout:    cfg.HTTP.ReadTimeout,
		WriteTimeout:   cfg.HTTP.WriteTimeout,
		IdleTimeout:    cfg.HTTP.IdleTimeout,
		MaxHeaderBytes: 1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("api server listening", "addr", srv.Addr, "db_driver", cfg.DB.Driver)
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case serveErr := <-errCh:
		return serveErr
	}
}

// newRepository selects the repository implementation from config.
// "fake" is the in-memory default for local development and the walking
// skeleton; "mysql" opens the pool and applies migrations. deployContext scopes
// the running_scenario rows this process owns.
func newRepository(cfg config.DBConfig, deployContext string) (repository, error) {
	switch cfg.Driver {
	case "fake":
		return fake.NewStore(), nil
	case "mysql":
		db, err := sql.Open("mysql", cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("open mysql: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return nil, fmt.Errorf("ping mysql: %w", err)
		}
		if err := mysqladapter.Migrate(ctx, db); err != nil {
			return nil, fmt.Errorf("migrate mysql: %w", err)
		}
		return mysqladapter.NewRepository(db).WithContext(deployContext), nil
	default:
		return nil, fmt.Errorf("db driver %q not supported", cfg.Driver)
	}
}

// startAutoPurge launches the idle-engine sweeper unless it is disabled
// (interval zero). It stops when ctx is cancelled.
func startAutoPurge(ctx context.Context, admin *adminapp.Service, cfg config.ClusterConfig) {
	if cfg.AutoPurgeInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(cfg.AutoPurgeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if purged, err := admin.AutoPurgeStale(ctx, cfg.AutoPurgeIdle); err != nil {
					slog.Warn("auto-purge sweep", "error", err)
				} else if len(purged) > 0 {
					slog.Info("auto-purged idle executions", "executions", purged)
				}
			}
		}
	}()
}

// newScheduler selects the Scheduler adapter. "fake" is in-memory; "k8s" builds
// a registry-backed Router: the control plane's own (in-cluster) client serves
// the default cluster and holds the credential Secrets, and any registered
// cluster is resolved to its own client on demand.
func newScheduler(cfg config.ClusterConfig, registry ports.ClusterRegistry) (ports.Scheduler, error) {
	switch cfg.Scheduler {
	case "fake":
		return fake.NewScheduler(), nil
	case "k8s":
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("k8s in-cluster config: %w", err)
		}
		client, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("k8s client: %w", err)
		}
		factory := k8sscheduler.NewClientFactory(client, k8sscheduler.DefaultDeploy{
			Namespace: cfg.Namespace, SidecarImage: cfg.SidecarImage, IngestURL: cfg.IngestURL,
		}, registry)
		return k8sscheduler.NewRouter(factory, k8sscheduler.Config{EnginePort: cfg.EnginePort}), nil
	default:
		return nil, fmt.Errorf("scheduler %q not supported", cfg.Scheduler)
	}
}

// newClusterService wires the cluster-registry application service backing the
// /api/clusters management endpoints. It is enabled only for the k8s scheduler
// with a configured credential-encryption key (needed to encrypt BYOC
// kubeconfigs at rest); otherwise it returns nil and the endpoints are disabled
// (a deployment that does not register clusters need not configure a key).
func newClusterService(cfg config.ClusterConfig, repo repository) (httpapi.ClusterService, error) {
	if cfg.Scheduler != "k8s" || cfg.CredentialKey == "" {
		return nil, nil
	}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("cluster registry: k8s in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("cluster registry: k8s client: %w", err)
	}
	cipher, err := secretbox.NewFromHex(cfg.CredentialKey)
	if err != nil {
		return nil, fmt.Errorf("cluster registry: credential key: %w", err)
	}
	return clusterapp.NewService(clusterapp.Deps{
		Registry:    repo,
		Prober:      k8sscheduler.Prober{},
		Credentials: k8sscheduler.NewHomeCredentialStore(client, cfg.Namespace),
		Runs:        repo,
		Cipher:      cipher,
		Parse:       k8sscheduler.ParsePortsKubeconfig,
		SecretName:  k8sscheduler.CredentialSecretName,
	}), nil
}

// newAuthProvider selects the authentication adapter. "none" authenticates
// every request as the fixed service-provider admin (local dev); "oidc"
// verifies bearer ID tokens against the issuer's JWKS, fetched once at
// startup; "demo" verifies the HMAC-signed persona cookie.
func newAuthProvider(ctx context.Context, cfg config.AuthConfig) (ports.AuthProvider, error) {
	switch cfg.Mode {
	case "none":
		return noauth.New("honryu"), nil
	case "oidc":
		keys, err := fetchJWKS(ctx, cfg.OIDC.JWKSURL)
		if err != nil {
			return nil, fmt.Errorf("oidc jwks: %w", err)
		}
		return oidc.New(keys, cfg.OIDC.Issuer, oidc.WithAudience(cfg.OIDC.Audience)), nil
	case "demo":
		profiles := make([]session.Profile, 0, len(cfg.Demo.Profiles))
		for _, p := range cfg.Demo.Profiles {
			profiles = append(profiles, session.Profile{
				ID: p.ID, Name: p.Name, Subject: p.Subject, Email: p.Email,
				Global: p.Global, Tenants: p.Tenants,
			})
		}
		return session.New([]byte(cfg.Demo.SigningKey), profiles)
	default:
		return nil, fmt.Errorf("auth mode %q not supported", cfg.Mode)
	}
}

// fetchJWKS retrieves and parses the JSON Web Key Set from url.
func fetchJWKS(ctx context.Context, url string) (*oidc.StaticKeySet, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return oidc.ParseJWKS(body)
}

// newObjectStore selects the ObjectStore adapter. "local" stores artifacts on
// the filesystem; "nexus" targets a Sonatype Nexus raw repository.
func newObjectStore(cfg config.StorageConfig) (ports.ObjectStore, error) {
	switch cfg.Driver {
	case "local":
		return local.New(cfg.Root, cfg.BaseURL), nil
	case "nexus":
		opts := []nexus.Option{}
		if cfg.Username != "" || cfg.Password != "" {
			opts = append(opts, nexus.WithBasicAuth(cfg.Username, cfg.Password))
		}
		return nexus.New(cfg.BaseURL, cfg.Repo, opts...), nil
	default:
		return nil, fmt.Errorf("storage driver %q not supported", cfg.Driver)
	}
}

func setupLogging(cfg config.LogConfig) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// startReconcile launches the stranded-run sweep unless it is disabled
// (interval zero). It stops when ctx is cancelled. Each pass finalizes open
// runs whose engines already finished -- nothing else ever will, and the
// report it writes is evidence-based.
func startReconcile(ctx context.Context, lifecycle *lifecycleapp.Service, cfg config.ClusterConfig) {
	if cfg.ReconcileInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(cfg.ReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := lifecycle.Reconcile(ctx); err != nil {
					slog.Warn("stranded-run reconcile", "error", err)
				}
			}
		}
	}()
}
