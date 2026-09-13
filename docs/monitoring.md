# Monitoring runbook (Prometheus + Grafana)

The chart ships a small, single-replica Prometheus and a Grafana that provisions its dashboards and datasource from the
image — both values-gated and **on by default** since phase 57 (`deploy/chart/honryu/values.yaml`, `prometheus.enabled`
/ `grafana.enabled`). Disable them as a pair: every dashboard queries the Prometheus datasource, so Grafana without
Prometheus is only empty panels.

## Reaching the UIs

Both services are ClusterIP-only — no Ingress route. Port-forward:

```sh
# Grafana (anonymous Viewer by default; admin login lives in the
# grafana-admin Secret — keys admin-user / admin-password):
kubectl -n honryu port-forward svc/honryu-grafana 3000:3000
open http://127.0.0.1:3000/

# Prometheus (operational surface: targets, TSDB status, manual queries):
kubectl -n honryu port-forward svc/prometheus 9090:9090
open http://127.0.0.1:9090/
```

(`honryu-grafana` is release-prefixed; the Prometheus Service is deliberately named literally `prometheus` — see "The
name contract" below. Substitute your release namespace for `honryu` if different.)

Grafana admin credentials are never baked into the image; the chart injects them from the Secret named by
`secrets.grafanaAdmin`. Note that rotating that Secret does **not** change the password on an existing install — Grafana
applies `GF_SECURITY_ADMIN_PASSWORD` only when it first creates the admin user; after that the credential lives in
`grafana.db` on the PVC.

## What each dashboard shows

Dashboards and the datasource are baked into the `honryu/grafana` image at build time (`grafana/Dockerfile`); the chart
mounts no dashboard ConfigMaps. All panels pin the datasource `honryu_prom`.

| Dashboard         | Shows                                                                                                                                                                                                  | Data path                                                                                                                                                                                                                                          |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **honryu**        | Per-run load results: response-status breakdowns, request rates, active threads, latency percentiles (p90/p99). Variables `executionID` / `scenarioID` / `label` / `runID` resolve from series labels. | Engine pods push run metrics through the ingest API into the api's `MetricsSink` (`internal/adapters/metrics/prometheus`), which registers `honryu_status_counter`, `honryu_threads_gauge`, `honryu_latency_*`. Scraped from the api's `/metrics`. |
| **honryu_perf**   | api process introspection: Go memstats, goroutines, GC, open fds, resident/virtual memory. Variables `namespace` / `pod` come from pod-discovery relabels.                                             | Standard `go_*` / `process_*` series exposed by promhttp on the api.                                                                                                                                                                               |
| **honryu_engine** | Intended: per-engine CPU and memory during a run. **Currently empty** — it queries `honryu_cpu_gauge` / `honryu_mem_gauge`, which no component in this repo registers.                                 | Known gap (recorded phase 57). Do not "fix" it by scraping engine pods: they are third-party jmeter/k6 images with no `/metrics` endpoint. The fix belongs in the engine sidecar, exporting gauges to the ingest API like every other run metric.  |

If `honryu` shows no runs at all, check the api target first (`kubectl -n honryu get pods` then Prometheus → Status →
Targets): engine metrics are visible only while (and after) the api that ingested them is being scraped.

## Adding a scrape target

Prometheus discovers targets by pod annotation, scoped to the release namespace only (`kubernetes_sd_configs` role
`pod`; the `honryu-prometheus` ServiceAccount holds just `list`/`watch` on pods there):

```yaml
annotations:
  prometheus.io/scrape: 'true'
  prometheus.io/path: /metrics # optional, default /metrics
  prometheus.io/port: '8080' # optional, default pod port
```

Annotate the pod template of whatever should be scraped and roll it. Only the api is annotated today — the calibrator
and scheduler serve no HTTP at all, so annotating them would only create permanently-down targets.

## Tuning

All knobs live under `prometheus:` / `grafana:` in `deploy/chart/honryu/values.yaml`:

- `prometheus.retention` (default `3d`) and `prometheus.size` (default `2Gi`): Prometheus is the live/short-retention
  surface by design — durable summaries are persisted to ReportStore (MySQL) at run completion. Growing the TSDB here
  contradicts that design rather than extending it.
- `prometheus.scrapeInterval` (default `15s`).
- Both deployments run `strategy: Recreate`, not RollingUpdate: Prometheus takes an exclusive lock on its TSDB directory
  and Grafana writes SQLite, so overlapping pods on one ReadWriteOnce volume deadlock or corrupt. Expect a few seconds
  of scrape gap on upgrades.
- Do not shrink `grafana.resources.requests.memory` below `384Mi`: the floor is the Grafana 12+ server (unified storage,
  bleve indexes, alerting), not the data — a cold start at 256Mi is recorded in the values comment as having thrashed at
  99.85% of the limit.

## Chart-render safety

Any change to these templates must render clean before commit:

```sh
helm template deploy/chart/honryu -f deploy/chart/honryu/values.yaml
helm lint deploy/chart/honryu -f deploy/chart/honryu/values.yaml

# Validate the rendered scrape config with the real binary (relabel regex
# errors pass helm silently):
helm template deploy/chart/honryu -f deploy/chart/honryu/values.yaml \
  -s templates/prometheus-configmap.yaml \
| docker run --rm -i --entrypoint promtool prom/prometheus:v3.7.2 \
  check config /dev/stdin
```

There is no Go test that renders the chart; the commands above are the gate.

## The name contract

`grafana/datasources/local.yml` is baked into the Grafana image and therefore cannot be Helm-templated. It hardcodes
`url: http://prometheus:9090` with datasource name `honryu_prom` — which is why `templates/prometheus-service.yaml`
names the Service literally `prometheus`, not fullname-prefixed. Renaming that Service (or the datasource) quietly
empties every dashboard; if you must change either, change `local.yml` and rebuild the image in the same change.
