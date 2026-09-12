package main

import (
	"context"
	"maps"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/config"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func TestNewProjectRepository_Fake(t *testing.T) {
	t.Parallel()
	repo, err := newRepository(config.DBConfig{Driver: "fake"}, "default")
	if err != nil {
		t.Fatalf("newRepository(fake): %v", err)
	}
	if repo == nil {
		t.Fatal("newRepository(fake) returned nil repo")
	}
}

func TestNewProjectRepository_Unsupported(t *testing.T) {
	t.Parallel()
	if _, err := newRepository(config.DBConfig{Driver: "postgres"}, "default"); err == nil {
		t.Fatal("newRepository(postgres): expected error, got nil")
	}
}

func TestNewProjectRepository_MySQL_Unreachable(t *testing.T) {
	t.Parallel()
	// Nothing listens on port 1, so the ping fails fast: covers the mysql
	// open-ok / ping-error wiring branch without needing a container.
	_, err := newRepository(config.DBConfig{
		Driver: "mysql",
		DSN:    "honryu:secret@tcp(127.0.0.1:1)/honryu?parseTime=true",
	}, "default")
	if err == nil {
		t.Fatal("newRepository(mysql, unreachable): expected error, got nil")
	}
}

func TestSetupLogging_AllVariants(t *testing.T) {
	// Not parallel: mutates the global slog default logger.
	for _, c := range []config.LogConfig{
		{Level: "debug", Format: "text"},
		{Level: "info", Format: "json"},
		{Level: "warn", Format: "json"},
		{Level: "error", Format: "text"},
		{Level: "unknown", Format: "unknown"}, // exercises the default arms
	} {
		setupLogging(c)
	}
}

func TestRun_ConfigError(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HONRYU_HTTP_PORT": "not-a-number"}
	err := run(context.Background(), func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("run with invalid config: expected error, got nil")
	}
}

func TestNewProjectRepository_MySQL_BadDSN(t *testing.T) {
	t.Parallel()
	// A DSN missing the slash separating the database name is rejected at
	// Open, before any connection is attempted.
	if _, err := newRepository(config.DBConfig{Driver: "mysql", DSN: "honryu:secret"}, "default"); err == nil {
		t.Fatal("newRepository(mysql, malformed DSN): expected error, got nil")
	}
}

// run() surfaces wiring failures from each construction stage, not just the
// config stage: every row holds the rest of the config valid and drives
// exactly one adapter constructor into its error return.
func TestRun_WiringErrors(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"HONRYU_DB_DRIVER":          "fake",
		"HONRYU_LOG_FORMAT":         "text",
		"HONRYU_RECONCILE_INTERVAL": "0",
	}
	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			"unreachable mysql dsn",
			map[string]string{
				"HONRYU_DB_DRIVER": "mysql",
				"HONRYU_DB_DSN":    "honryu:secret@tcp(127.0.0.1:1)/honryu?parseTime=true",
			},
		},
		{"unsupported storage driver", map[string]string{"HONRYU_STORAGE_DRIVER": "s3"}},
		{"unsupported scheduler", map[string]string{"HONRYU_SCHEDULER": "nope"}},
		// Note: no row here may reach run()'s metrics sink (promsink.New on
		// prometheus.DefaultRegisterer) -- it registers on the process-global
		// registry, so a second run() instance in the same test binary panics
		// with a duplicate-collector registration. Later wiring failures (auth
		// mode, serve errors) are therefore not drivable from unit tests.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := maps.Clone(base)
			maps.Copy(env, tc.env)
			if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
				t.Fatal("run: expected a wiring error, got nil")
			}
		})
	}
}

func TestStartReconcile_TicksUntilCancelled(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	lifecycle := lifecycleapp.NewService(store, fake.NewScheduler(), fake.NewObjectStore(), lifecycleapp.StaticImage("honryu/jmeter:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startReconcile(ctx, lifecycle, config.ClusterConfig{ReconcileInterval: 5 * time.Millisecond})
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond) // let the in-flight tick's Reconcile return
}

func TestRealMain_ConfigError(t *testing.T) {
	// t.Setenv marks the test non-parallel and restores the env afterwards.
	t.Setenv("HONRYU_HTTP_PORT", "not-a-number")
	if err := realMain(); err == nil {
		t.Fatal("realMain with invalid config: expected error, got nil")
	}
}

func TestRun_ServesAndShutsDownCleanly(t *testing.T) {
	t.Parallel()

	port := freePort(t)
	env := map[string]string{
		"HONRYU_HTTP_PORT":  strconv.Itoa(port),
		"HONRYU_DB_DRIVER":  "fake",
		"HONRYU_LOG_FORMAT": "text",
	}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }) }()

	// Wait until the server answers, then trigger graceful shutdown.
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitReady(t, base+"/healthz")

	resp, err := http.Get(base + "/api/projects")
	if err != nil {
		t.Fatalf("GET /api/projects: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/projects status = %d, want 200", resp.StatusCode)
	}

	// The embedded SPA build (web.Dist, unwrapped by run()'s fs.Sub) is
	// served for "/" -- proves the embed and fs.Sub wiring actually work,
	// not just that they compiled.
	staticResp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	_ = staticResp.Body.Close()
	if staticResp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", staticResp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error on shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not shut down within 15s")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s not ready in time", url)
}
