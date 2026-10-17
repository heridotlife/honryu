# Phase 107 tasks

1. **Migration 0087 + domain + store** — SQL file, Report.Starred/Note fields,
   reportColumns + scans, SetStarred/SetNote (port + mysql + fake), contract
   tests. One commit.
2. **HTTP handlers + router** — PUT star, PUT note, validation (bool strict,
   note ≤2000 runes), auth wiring, handler tests incl. 404 on unknown run.
   One commit.
3. **Web API client + Reports UI** — starRun/setRunNote client calls, star
   toggle + note inline edit + starred filter chip on Reports rows, vitest.
   One commit.
4. **RunCompare integration** — starred-first picker, single-star preselect,
   starred badge in slots, vitest. One commit.
5. **Docs/ledger** — PROGRESS.md append (operator does the add -f), NOTES if
   needed. One commit only if content changes.
