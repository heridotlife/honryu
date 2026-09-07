# Phase 26 tasks

- T1 chart hygiene: Chart.yaml appVersion phase20→phase26, version 0.1.0→0.2.0; values.yaml sidecar tag phase16→phase26
- T2 sidecar image: build+push honryu-sidecar:phase26 (deploy/honryu/Dockerfile --build-arg CMD=sidecar; cmd/sidecar exists)
- T3 PDF export: GET /api/runs/{id}/export?format=pdf (minimal PDF from report structure, no new heavy deps — hand-rolled PDF writer acceptable); UI anchor data-testid=export-pdf; go tests + vitest + layout-check
- T4 close: PROGRESS.md, gates, deploy phase26, live-validate /api/clusters still 12 + PDF download works
