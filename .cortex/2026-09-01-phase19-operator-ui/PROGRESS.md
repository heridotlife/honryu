# Phase 19 — Operator UI — execution progress

Branch: feat/phase19-operator-ui (off develop). One task per executor tick;
quota hold at >=75% gateway quota (cron f3c38d88b06f). Never push, never
merge, never touch main.

## Landed

| Task | Commit | Gates | What it is |
|------|--------|-------|------------|
| G1 | 06d470d | build/vet/test/lint, openapi route table | GET /api/executions, caller-scoped, newest first |
| G2 | 02163d0 | build/vet/test/lint, openapi | GET /scenarios/{id}/requests, byte-exact text/yaml |
| G3 | 1edf2e5 | build/vet/test/lint, openapi | raw text/yaml body on PUT /scenarios/{id}/requests |
| G4 | f01e5fe | build/vet/test/lint (0 issues), MySQL integration contract 12.9s | line-anchored Diagnostic type; fragment rejections carry {message, diagnostics[]}; single validation path requestDiagnostics shared by store + future validate; openapi 400 -> DiagnosticsError |

| G5 | df8650e | build/vet/test/lint 0 issues | POST /scenarios/{id}/requests/validate, store-path parity, valid+diagnostics envelope |

| G8 | 96344e8 | build/vet/test 0 fail/lint 0 issues, golden updated | fragment headers/timeout/keepalive carried to engine, telemetry wins collision |

| G6 | 21ffa1d | build/vet/test 0 fail/lint 0 issues | uncompiled keys -> info diagnostics, wording test |

| G7 | 4cbb41f | build/vet/test 0 fail/lint 0 issues | JSON body on PUT /executions/{id}/config, multipart unchanged |

| R1 | ad3c2e7 | tsc -b clean, vitest 110/110, dist 336K, **cluster verified** | /executions list + /status redirect, linked to /executions/:id |

| R2 | 0433701 | tsc clean, vitest 117/117, dist 344K | execution hub, phase-driven controls, SSE reused, deep-linkable |

| R3 | 68a6e15 | tsc clean, vitest 120/120, dist 348K | past runs + engine log tail on hub, links to /reports/:runId |

| R4 | cdc1ce0 | tsc clean, vitest 124/124, dist 764K (<1MB) | CodeMirror 6 editor over G2/G3, putRaw primitive |

| R5 | 8d8f83f | tsc clean, vitest 131/131, byte-equality tests, dist 864K | AST form lens, headers table, indent-preserving serialization |

| R6 | 83606be | tsc clean, vitest 139/139, dist 868K | two-layer validation: client parse + G5 debounce, G6 info non-blocking |

| R7 | 8b62e07 | tsc clean, vitest 146/146, dist 872K | capacity panel: fan-out verdict per status, job progress polling |

| R8 | 668f1ec | tsc clean, vitest 151/151 | save-time guard: warn on fresh profile only, names qps/pod + calibrated_at |

| R9 | a3c3f78 | tsc clean, vitest 159/159, dist 880K | one-form new-test flow, step-named failures, clamp + rampup guards |

## Next task

**Phase complete** — all 17 tasks landed; branch NOT pushed (standing rule) (service-only:
internal/app/scenarioapp/service.go + tests, no handler/route/openapi).
Decode with KnownFields(true), surface unknown taurus.Scenario keys
(e.g. think-time) as severity: info; such a document still validates AND
still stores. Message wording must be EXACTLY "stored but not compiled and
will not affect the run" (test asserts it). Depends on G5 + G8, both
landed. NOTE: ValidateRequests now signals rejection via the RETURNED
*InvalidRequestsError (wrapping ErrRequestsInvalid), not diagnostics with
nil error -- G6 info findings ride in the diagnostics of a VALID (nil-error)
validate response.

## After that

G7 -> G8 -> R1 (R1 is the SEAM: verify on cluster with kubectl
port-forward + curl /api/executions returns [] before R2).

## Notes / traps

- Mirror-stale trap: after remote-side edits, re-scp before next push.
- Commit messages via script + git commit -F /tmp/msg.txt (apostrophe trap).
- yaml.v3 v3.0.1 has no exported SyntaxError; parse failures are plain
  errors with "line N:" embedded -- diagnosticsFromYAMLError parses it.
- web/dist <1MB gate applies from R4 on.
- G5 contract (df8650e): validate success = {valid: true, diagnostics: []};
  rejection = 400 DiagnosticsError envelope, identical on PUT and POST.
  TestValidateRequestsParity (router_phase1_test.go) pins the seam.
