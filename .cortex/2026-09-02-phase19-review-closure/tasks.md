# Phase 19b — tasks

Six tasks. **F1 first**: it is the only finding that is currently wrong in
production, and it is independent of everything else.

---

### F1. Reconcile the chart with the running image
- **Files:** `deploy/chart/honryu/values.yaml`,
  `deploy/chart/honryu-homelab-values.yaml`, cluster state (out-of-band
  `honryu-api-seam` Deployment/Service)
- **Scope expanded 2026-09-03, confirmed with the user.** Investigation found
  two things beyond "point the chart at the running api image":
  - **Debris.** A scratch `honryu-api-seam` Deployment + Service exist outside
    helm — no volumes, not selected by the real Ingress-facing Service, and
    already crash-looping (`CreateContainerConfigError` /
    `ImagePullBackOff`). Delete both.
  - **A real correctness gap, not just a stale pointer.** `cmd/scheduler`
    (`scheduler/main.go:199`, `lifecycle.Deploy` for fired schedules) and
    `cmd/calibrator` (`calibrationapp/step.go:186`, every calibration step)
    are separate binaries that each statically link their own copy of the
    G8-fixed compile path. They were still on `phase16b`/`phase16` while
    `honryu-api` ran phase 19 code, meaning a scheduled or calibration-driven
    deploy silently would **not** get G8's header/timeout/keepalive fidelity
    while a manually-triggered one would — same source, different binaries,
    different behavior. `cmd/sidecar` is untouched by phase 19 (confirmed via
    diff) and is not rebuilt.
- **Criteria:** `honryu-api-seam` Deployment and Service no longer exist;
  `honryu-api`, `honryu-scheduler` and `honryu-calibrator` all run an image
  built from the phase 19 tree (`deploy/honryu/Dockerfile`,
  `--build-arg CMD={api,scheduler,calibrator}`); chart tags for all three match
  what is running; `helm get manifest honryu -n honryu | grep "honryu-\(api\|scheduler\|calibrator\):"`
  matches each Deployment's actual image; a `helm upgrade` completes **without**
  rolling any of the three into a different image afterward (verify pod
  `restartCount` and image, not the upgrade's exit code); `make helm-lint`
  passes
- **Satisfies:** spec criterion 10; finding 1; closes the scheduler/calibrator
  parity gap found during execution
- **Depends on:** —

### F2. Report modelled-but-uncompiled keys
- **Files:** `internal/app/scenarioapp/diagnostics.go`,
  `internal/app/scenarioapp/service_test.go`
- **Criteria:** an explicit, commented set names the keys `taurus.Scenario`
  models but `compileScenario` never reads from a fragment — today
  `data-sources` and `script`; each produces an `info` diagnostic anchored to
  its own line with the same stored-but-not-compiled wording; `think-time`
  keeps its existing `KnownFields`-sourced diagnostic unchanged; a fragment with
  all three yields three diagnostics
- **Satisfies:** spec criteria 1, 2, 3; finding 2
- **Depends on:** —

### F3. Guard the uncompiled-key set against the compile path
- **Files:** `internal/domain/compile/compile_test.go` or
  `internal/app/scenarioapp/diagnostics_test.go`
- **Criteria:** a test pins which `taurus.Scenario` fields `compileScenario`
  populates from `ScenarioInput`, and fails if a field named in F2's set starts
  being compiled (or a newly-uncompiled field is not added); the failure message
  says which file to update. The set is hand-maintained by design — it is a fact
  about the compile path, not derivable from the struct
- **Satisfies:** spec criterion 4
- **Depends on:** F2

### F4. Collapse the triple decode, cover Error(), drop the type name
- **Files:** `internal/app/scenarioapp/diagnostics.go`,
  `internal/app/scenarioapp/diagnostics_test.go`
- **Criteria:** `uncompiledKeyDiagnostics` strict-decodes the document **once**
  (delete the dead `_ = errors.As(isUnknownFieldErrSource(raw), &typeErr)` and
  the now-unused helper); `InvalidRequestsError.Error()` has direct tests for
  both the line-anchored and unanchored branches; no diagnostic message contains
  a Go type name — `"think-time not found in type taurus.Scenario: …"` becomes
  wording aimed at a person; existing diagnostics are otherwise byte-identical,
  proven by the F2 tests still passing unchanged
- **Satisfies:** spec criteria 5, 6, 7; findings 3, 4, 5
- **Depends on:** F2, F3

### F5. Add the phase 19 end-to-end test
- **Files:** `test/e2e/phase19_e2e_test.go`
- **Criteria:** follows the shape of `portable_scenario_e2e_test.go`; drives
  create project → scenario → requests fragment carrying a header → config →
  deploy → read the compiled shard config back via
  `GET /api/runs/{id}/scenarios/{sid}/shards/{shard}/config`; asserts the
  fragment's header is present **alongside** `traceparent` and `baggage`, not
  instead of them, and that a fragment `traceparent` loses the collision;
  asserts the validate endpoint's diagnostics for an uncompiled key; runs inside
  the existing 30m suite timeout
- **Satisfies:** spec criteria 8, 9; findings 6, 7 (the automated half)
- **Depends on:** F2, F4

### F6. Push the branch and get CI green
- **Files:** — (no source change)
- **Criteria:** `feat/phase19-operator-ui` is pushed; CI, Security and CodeQL all
  pass; `cover-gate` ≥ 90% in CI, not only locally
- **Satisfies:** spec criteria 11, 12
- **Depends on:** F1, F2, F3, F4, F5

---

## Sequencing

```
F1 ─────────────────────────────► (independent, do first)

F2 ─► F3 ─► F4 ─► F5 ─┬─────────► F6
F1 ───────────────────┘
```

**F1 is urgent rather than large.** Until it lands, any `helm upgrade` — for a
Grafana tweak, a resource change, anything — silently reverts the API to
`phase16c` and takes `GET /api/executions` and the new SPA with it.

**The manual G8 ship gate** (plan Verification step 3) is separate from F5: F5
proves the compile path in the e2e harness, the ship gate proves it on the real
cluster with real engine pods. Both, not either.
