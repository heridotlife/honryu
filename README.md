# 奔流 (Honryu)

[![CI](https://github.com/heridotlife/honryu/actions/workflows/ci.yml/badge.svg)](https://github.com/heridotlife/honryu/actions/workflows/ci.yml)
[![CodeQL](https://github.com/heridotlife/honryu/actions/workflows/codeql.yml/badge.svg)](https://github.com/heridotlife/honryu/actions/workflows/codeql.yml)
[![Security](https://github.com/heridotlife/honryu/actions/workflows/security.yml/badge.svg)](https://github.com/heridotlife/honryu/actions/workflows/security.yml)
[![golangci-lint](https://img.shields.io/badge/golangci--lint-enabled-4c1)](.golangci.yml)
[![codecov](https://codecov.io/gh/heridotlife/honryu/graph/badge.svg)](https://codecov.io/gh/heridotlife/honryu)
[![Go Version](https://img.shields.io/github/go-mod/go-version/heridotlife/honryu)](go.mod)
[![License](https://img.shields.io/github/license/heridotlife/honryu)](LICENSE)

Honryu is a cloud-native, distributed load-testing platform written in Go. It
orchestrates load-generation engines (JMeter and k6) on Kubernetes, streams live
metrics to Prometheus, and exposes a REST API for managing projects, scenarios,
and runs — with pluggable authentication, RBAC, and multi-tenancy.

The codebase is a test-driven, hexagonal (ports-and-adapters) implementation:
the domain is pure, use-cases depend on interfaces (ports), and every adapter is
validated by the same conformance suite as its in-memory fake. CI enforces a
**≥90%** coverage gate over production packages.

## Architecture

```
cmd/
  api/          REST API server (wires adapters -> app)
  controller/   orchestrator entrypoint
  agent/        engine-side metrics agent
internal/
  domain/       pure business types + rules, zero I/O imports
                (project, scenario, execution, loadprofile, run, engine,
                 usage, account, rbac, tenant)
  ports/        interfaces the app depends on
                (Repository, Scheduler, Executor, ObjectStore,
                 AuthProvider, MetricsSink, EventBus, AuditLog, ...)
    fake/       in-memory port implementations for fast tests
    *test/      reusable conformance suites run against every adapter
  app/          use-cases orchestrating the domain over ports
                (project, scenario, execution, lifecycle, metrics, usage,
                 admin, auth, tenant)
  adapters/
    httpapi/          inbound REST adapter (net/http)
    repo/mysql/       MySQL repository adapter
    scheduler/k8s/    Kubernetes engine scheduler
    executor/jmeter/  JMeter executor
    executor/k6/      k6 executor
    storage/local/    filesystem object store
    storage/nexus/    Sonatype Nexus raw-repo object store
    auth/{noauth,token,oidc}/   authentication providers
    metrics/prometheus/         Prometheus metrics sink
    eventbus/memory/            in-process event bus
    audit/memory/               audit log
  config/       typed, injected configuration (no globals)
migrations/     embedded, ordered SQL migrations
test/
  dbtest/       testcontainers MySQL helper (integration/e2e only)
  e2e/          full-stack end-to-end tests
scripts/
  coverage.sh   coverage gate used by CI
```

### Component overview

Requests enter through the inbound HTTP adapter and flow into the pure
application core; the core reaches the outside world only through ports, each
backed by an interchangeable adapter (and an in-memory fake in tests).

```mermaid
flowchart TB
    client["REST client / SPA"]

    subgraph core["Application core — no infrastructure imports"]
        direction TB
        api["httpapi<br/>(inbound adapter)"]
        app["use-cases<br/>project · scenario · execution · lifecycle<br/>metrics · usage · admin · auth · tenant"]
        domain["domain<br/>pure types + rules"]
        ports{{"ports (interfaces)<br/>Repository · Scheduler · Executor · ObjectStore<br/>AuthProvider · MetricsSink · EventBus · AuditLog"}}
        api --> app
        app --> domain
        app --> ports
    end

    client -->|HTTP + SSE| api

    ports --> repo["repo/mysql"]
    ports --> sched["scheduler/k8s"]
    ports --> exec["executor<br/>jmeter · k6"]
    ports --> store["storage<br/>local · nexus"]
    ports --> authp["auth<br/>noauth · token · oidc"]
    ports --> metrics["metrics/prometheus"]

    repo --> db[("MySQL")]
    sched --> k8s["Kubernetes"]
    exec --> engines["JMeter / k6 engines"]
    store --> blob[("Filesystem / Nexus")]
    metrics --> prom[("Prometheus")]
    authp --> idp["OIDC provider"]
```

**Principles**

- The domain imports no infrastructure; use-cases depend on ports, not adapters.
- Every port ships an in-memory fake. Real adapters must pass the *same*
  conformance suite as the fake (`internal/ports/*test`), keeping them
  interchangeable.
- Configuration and collaborators are injected — there is no global state.

## Test lifecycle

A load test moves through **deploy → trigger → stream → stop → purge**. Every
request is authenticated and (when RBAC is enabled) authorized against the
caller's tenant before the lifecycle use-case orchestrates the scheduler and
executor; metrics stream back live over SSE while the test runs.

```mermaid
sequenceDiagram
    actor User
    participant API as httpapi
    participant Auth as auth (OIDC / RBAC)
    participant LC as lifecycle use-case
    participant Sched as scheduler (k8s)
    participant Exec as executor (JMeter / k6)
    participant Eng as engines
    participant Prom as Prometheus

    User->>API: POST /executions/{id}/deploy
    API->>Auth: authenticate + authorize
    Auth-->>API: account (tenant-scoped)
    API->>LC: Deploy
    LC->>Sched: create engine pods
    Sched-->>Eng: schedule

    User->>API: POST /executions/{id}/trigger
    API->>LC: Trigger
    LC->>Exec: Trigger(config) per engine
    Exec->>Eng: start test

    loop while running
        Eng-->>Exec: metric samples
        Exec-->>LC: engine.Metric
        LC->>Prom: record
    end

    User->>API: GET /executions/{id}/stream
    API-->>User: live metrics (SSE)

    User->>API: POST /executions/{id}/stop
    LC->>Exec: Stop
    User->>API: POST /executions/{id}/purge
    LC->>Sched: delete engine pods
```

### Engine idle reaper (activity-aware TTL)

Deployed engine pods outlive their runs — a finished bzt pod stays Ready
forever — so without a reaper they accumulate until someone purges them by
hand. When `HONRYU_ENGINE_IDLE_TTL` is set (default: disabled), the scheduler
binary runs an activity-aware reaper: every deploy, run start, run completion
(natural finalize **or** stop), and calibration step restamps the execution's
`last_activity_at`; a sweep on the scheduler tick tears down the pods of
executions idle beyond the TTL. The teardown is **pod-only** — the same path
purge walks (teardown duties + engine log capture) minus the metric-series
drop — so the execution row, runs, history, and reports all survive, and a
re-deploy recreates the engines. This makes "purge-on-finalize" implicit: a
naturally-completed run keeps its engines warm for exactly one TTL (fast
re-runs), then they are reaped. Guards: an open run marker or an active
calibration job is never reaped; executions deployed before the clock existed
(orphaned pods) fall back to the pods' own age, so the first pass heals them.

## Testing

Three layers, gated in CI at **≥90%** coverage over production packages:

| Lane | Command | Needs Docker |
|------|---------|:---:|
| Unit | `make test` | no |
| Integration (adapter contract tests) | `make integration` | yes |
| End-to-end (real HTTP → services → MySQL) | `make e2e` | yes |
| Coverage gate (all of the above) | `make cover-gate` | yes |

Integration/e2e tests are guarded by the `integration` / `e2e` build tags, so
the default build and unit lane stay free of Docker dependencies.

## Run locally

```bash
# Walking skeleton against the in-memory repository:
go run ./cmd/api                 # serves :8080

curl localhost:8080/healthz      # {"status":"ok"}
curl localhost:8080/api/projects # []
curl localhost:8080/metrics      # Prometheus exposition
```

## Configuration

Configuration is read from `HONRYU_*` environment variables (see
`internal/config`); there are no config files or global singletons. Everything
has a local-dev default, so `go run ./cmd/api` works with no environment set.

| Variable | Default | Purpose |
|----------|---------|---------|
| `HONRYU_HTTP_PORT` | `8080` | API listen port |
| `HONRYU_HTTP_TRIGGER_READY_POLL` | `2s` | trigger's engine-readiness poll interval |
| `HONRYU_HTTP_TRIGGER_READY_TIMEOUT` | `2m` | trigger's bounded wait for just-deployed engines |
| `HONRYU_RECONCILE_INTERVAL` | `1m` | stranded-run sweep interval (`0` disables) |
| `HONRYU_RUN_RECONCILE_AFTER` | `2h` | age at which an open, unreported, engine-less run may be reconciled as aborted (`0` disables) |
| `HONRYU_ENGINE_IDLE_TTL` | `0` | engine idle TTL for the scheduler-side activity-aware reaper: pods of executions with no activity for this long are torn down (records survive; `0` disables) |
| `HONRYU_DB_DRIVER` | `fake` | `fake` (in-memory) or `mysql` |
| `HONRYU_DB_DSN` | – | MySQL DSN (required when driver is `mysql`) |
| `HONRYU_STORAGE_DRIVER` | `local` | `local` or `nexus` |
| `HONRYU_STORAGE_ROOT` | `storage-data` | filesystem root (local store) |
| `HONRYU_STORAGE_BASE_URL` | – | retrieval base URL (local) / Nexus server URL |
| `HONRYU_NEXUS_REPO` | – | Nexus raw repository (required for `nexus`) |
| `HONRYU_NEXUS_USERNAME` / `_PASSWORD` | – | Nexus basic-auth credentials |
| `HONRYU_SCHEDULER` | `fake` | `fake` or `k8s` |
| `HONRYU_EXECUTOR` | `fake` | `fake`, `jmeter`, or `k6` |
| `HONRYU_ENGINE_IMAGE` | `honryu/jmeter:latest` | engine container image |
| `HONRYU_AUTH_MODE` | `none` | `none` (fixed admin) or `oidc` |
| `HONRYU_APM_LINK_TEMPLATES` | – | JSON array of `{name, urlTemplate}` APM link-outs (`{{correlation_id}}`, `{{execution_id}}`, `{{run_id}}`, `{{project_id}}` placeholders); unknown placeholders fail startup |
| `HONRYU_ENABLE_RBAC` | `false` | enable tenant-scoped RBAC |
| `HONRYU_OIDC_ISSUER` / `_AUDIENCE` / `_JWKS_URL` | – | OIDC ID-token verification (required for `oidc`) |
| `HONRYU_MAX_ENGINES` | `500` | per-execution engine guardrail |
| `HONRYU_LOG_LEVEL` / `_FORMAT` | `info` / `json` | structured logging |

Run against MySQL with the JMeter executor on Kubernetes, for example:

```bash
HONRYU_DB_DRIVER=mysql HONRYU_DB_DSN='user:pw@tcp(db:3306)/honryu' \
HONRYU_SCHEDULER=k8s HONRYU_EXECUTOR=jmeter \
HONRYU_AUTH_MODE=oidc HONRYU_ENABLE_RBAC=true \
HONRYU_OIDC_ISSUER=https://issuer.example \
HONRYU_OIDC_JWKS_URL=https://issuer.example/.well-known/jwks.json \
go run ./cmd/api
```

Migrations are embedded and applied automatically on startup when the MySQL
driver is selected.

## License

See [LICENSE](LICENSE).
