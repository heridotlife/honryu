# e2e boot harness (manual gate)

`boot.e2e.mjs` drives real Chromium against a **live** deployment and asserts
the operator-SPA boot contract end to end. It exists because the phase49
redirect-loop regression shipped with a green unit suite: nothing exercised
the unauthenticated first paint, and vitest (jsdom) computes no real
navigation.

## What it covers

- **A. Unauthenticated boot** — `/` renders the "Who's operating?" picker
  within 10s, exactly **one** main-frame document navigation happens (a
  reload loop would produce endless ones), zero `pageerror` events.
- **B. Login** — clicking the `alice` persona SPA-navigates away from `/`
  without reloading the document.
- **C. Session death** — clearing cookies then clicking a nav link lands
  back on `/` (the once-per-page-load dead-session redirect latch).
- **D. Purge two-step** — the destructive Purge control arms first
  ("Confirm purge?") with **zero** `/purge` requests, then fires **exactly
  one** on confirm. Skipped (pass) when nothing is purgeable (demo drift).
- **E. Row sanity** — no `/executions` list row leaks a raw ISO timestamp
  (phase49 regression guard).

## How to run

```sh
cd web
bun run e2e                                        # default base URL below
HONRYU_E2E_BASE_URL=https://other-host bun run e2e # point elsewhere
```

Chromium is discovered from the Playwright cache (same scheme as
`scripts/layout-check.js`); override with `CHROMIUM_PATH`.

## Why it is manual

It needs a live environment — a running API serving the embedded SPA with
demo auth enabled, and reachable engines for the purge scenario — so it is
**not** wired into `bun run test` or CI. Run it by hand before
operator-facing releases; any `FAIL` exits non-zero (`SKIP` still passes).
