/**
 * Layout regression check for the operator SPA, at real viewport widths.
 *
 * Exists because jsdom -- what `vitest run` uses -- computes no geometry, so
 * the unit suite cannot see a layout bug. It missed a real one: the mobile
 * nav drawer was hidden with `invisible` while left in normal document flow,
 * so it still reserved its full height. <nav> measured 370px against
 * h-16's 64px and <main> started at y=370 on every page -- a ~306px band of
 * nothing above all content, about 46% of a 390x664 viewport. Nothing in the
 * repo could have caught that; this script can.
 *
 * Usage (from web/):
 *   bun run layout-check                       # against LAYOUT_CHECK_URL or the default
 *   LAYOUT_CHECK_URL=https://honryu.pve.heri.life bun run layout-check
 *   bun run layout-check -- --screenshots      # also write PNGs to .layout-check/
 *
 * Phase 20 added a per-persona pass: at `/` it selects each demo profile
 * and asserts the nav that persona renders matches its permission map
 * (inline links everywhere, drawer links at mobile). A target that offers
 * no demo profiles -- `vite preview` has no API, non-demo deployments have
 * no picker -- skips that section honestly; the layout invariants still
 * run. Against the served SPA (`go run ./cmd/api` embeds web/dist) the
 * persona section runs for real.
 *
 * Phase 21 (task 11) extended the persona pass with the new surfaces:
 * /executions/:id/compare is asserted for every read-capable persona
 * (layout invariants + heading always; delta table and p95 overlay only
 * when the target's API offers an execution with two finalised runs), and
 * the Reports page's time-series / requested-vs-achieved / per-label
 * sections are asserted once, for the viewer persona, against a run with
 * series and labels. Data is discovered read-only over the same endpoints
 * the pages use; a target without such data (vite preview, a fresh stack)
 * skips the data-dependent assertions honestly. Seeding is deliberately
 * OUT of this script's design: it reads deployments, it does not build
 * them -- point it at a demo stack that already ran something (locally:
 * cmd/api in demo mode + an ingest-driven run, as the phase 21 close did).
 *
 * Phase 22 (task 10) extended the persona pass with the tier-2 surfaces:
 * the Execution page's live section (heading + charts-or-idle-placeholder,
 * asserted only while the discovered execution actually runs -- the section
 * renders for no finished eventless execution, and this script never seeds
 * runs), the NewTest page's stage-editor columns (Concurrency and Duration;
 * the editor renders for every persona, only the create button is gated),
 * and the Clusters page's Capacity column (alice alone holds system:admin).
 * The Execution page holds an open metric stream (SSE) for any valid id,
 * so networkidle never settles there -- it settles on domcontentloaded.
 *
 * Phase 23 (task 7) added the keyboard/export affordances: every shell
 * route must carry the skip link (a[href="#main"]), the FIRST Tab from
 * the page body must land on it AND make it visible (checked once, on
 * the first route of the first viewport -- every route shares the shell,
 * so the DOM-order contract is identical everywhere; visibility is the
 * Tailwind sr-only/focus:not-sr-only + focus:absolute ordering contract,
 * which only a real layout engine can confirm), and alice asserts the
 * report page's Export CSV/JSON + Copy link and the Execution page's
 * Copy link -- honest skips when the target has no discovered data.
 *
 * Needs a Chromium binary. Playwright's cache is found automatically; set
 * CHROMIUM_PATH to point elsewhere. Provision with:
 *   bunx playwright install chromium
 *
 * Exits non-zero on the first failed assertion, so it works as a gate.
 */
import { chromium, devices } from 'playwright-core';
import { existsSync, mkdirSync, readdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const BASE_URL = (process.env.LAYOUT_CHECK_URL || 'http://localhost:4173').replace(/\/$/, '');
const WANT_SCREENSHOTS = process.argv.includes('--screenshots');
const SHOT_DIR = '.layout-check';

/** Routes the nav exposes -- every one carries the shared DashboardLayout.
 * `/` is the picker (also under the layout: the nav renders, empty, until
 * a persona is selected).
 *
 * Parameterised routes cannot live here: their ids only exist once a
 * persona's session can read data. Phase 21's additions --
 * /executions/:id/compare and /reports/:runId with live sections -- are
 * asserted in the per-persona pass below, where the session is active. */
const ROUTES = ['/', '/reports', '/reservations', '/status', '/campaigns', '/clusters'];

/**
 * The demo personas (deploy/chart/honryu-homelab-values.yaml) and the exact
 * nav each must see. Derived from DefaultCatalog the same way navItemsFor
 * is: an entry survives only if the persona's permission map grants it --
 * Alice everything, Bob/Carol the three read surfaces, Dave plus campaigns
 * but never clusters (AC4). Nav visibility is a function of the session
 * now, so one constant cannot assert it for everyone (task 24).
 */
const PERSONAS = [
  { id: 'alice', hrefs: ['/reports', '/executions', '/reservations', '/campaigns', '/clusters'] },
  { id: 'bob', hrefs: ['/reports', '/executions', '/reservations'] },
  { id: 'carol', hrefs: ['/reports', '/executions', '/reservations'] },
  { id: 'dave', hrefs: ['/reports', '/executions', '/reservations', '/campaigns'] },
];

/**
 * Widths worth checking: below Tailwind's `md` (768px) the drawer is the only
 * nav, at and above it the inline links are and the drawer is `md:hidden`.
 * Both sides of that boundary can regress independently.
 */
const VIEWPORTS = [
  { name: 'mobile-390', width: 390, height: 664, isMobile: true },
  { name: 'tablet-768', width: 768, height: 1024, isMobile: false },
  { name: 'desktop-1280', width: 1280, height: 800, isMobile: false },
];

/**
 * Phase 21 (task 11): the persona that asserts the Reports page's new
 * data-bearing sections (time-series charts, per-label table). carol is
 * the tenant viewer -- the least-privileged persona that can still read
 * reports, so if the sections render for her they render for everyone
 * above her.
 */
const SECTION_PERSONA = 'carol';

/**
 * The nav is h-16 (64px) plus a 1px bottom border. Allow a little slack for
 * sub-pixel rounding, but nowhere near enough to let a reserved drawer band
 * (hundreds of px) slip through.
 */
const NAV_HEIGHT_MAX = 72;

function findChromium() {
  if (process.env.CHROMIUM_PATH) {
    return process.env.CHROMIUM_PATH;
  }
  const cache = join(homedir(), '.cache', 'ms-playwright');
  if (!existsSync(cache)) {
    return null;
  }
  // Layouts differ across playwright versions and platforms.
  const candidates = ['chrome-linux64/chrome', 'chrome-linux/chrome', 'chrome-mac/Chromium.app/Contents/MacOS/Chromium'];
  for (const dir of readdirSync(cache).filter((d) => d.startsWith('chromium-'))) {
    for (const rel of candidates) {
      const full = join(cache, dir, rel);
      if (existsSync(full)) {
        return full;
      }
    }
  }
  return null;
}

const failures = [];

/** Set once the first-Tab focus check has run (see the header comment:
 * once per run is enough -- the shell DOM order is shared by every route). */
let focusCheckDone = false;
function check(label, ok, detail) {
  if (ok) {
    console.log(`  ok   ${label}`);
  } else {
    console.log(`  FAIL ${label}${detail ? ` -- ${detail}` : ''}`);
    failures.push(`${label}${detail ? `: ${detail}` : ''}`);
  }
}

/** The three layout invariants every DashboardLayout route must hold. */
function checkLayout(routeLabel, geo) {
  check(
    `${routeLabel} nav height <= ${NAV_HEIGHT_MAX}px`,
    geo.navHeight !== null && geo.navHeight <= NAV_HEIGHT_MAX,
    `got ${geo.navHeight}px`
  );
  check(
    `${routeLabel} main sits directly under nav`,
    geo.mainTop !== null && Math.abs(geo.mainTop - geo.navHeight) <= 2,
    `main top ${geo.mainTop}px vs nav height ${geo.navHeight}px`
  );
  check(
    `${routeLabel} no horizontal overflow`,
    geo.scrollWidth <= geo.innerWidth + 1,
    geo.overflowing.length ? geo.overflowing.join(', ') : `scrollWidth ${geo.scrollWidth} > ${geo.innerWidth}`
  );
}

/** Collects console errors and page errors into `sink` (task 11: the new
 * pages must render without console errors, which jsdom tests cannot see
 * -- e.g. a chart component throwing only in a real layout pass). Returns
 * an unwatch function. */
function watchConsole(page, sink) {
  const onConsole = (msg) => {
    if (msg.type() === 'error') {
      sink.push(msg.text());
    }
  };
  const onPageError = (err) => sink.push(String(err));
  page.on('console', onConsole);
  page.on('pageerror', onPageError);
  return () => {
    page.off('console', onConsole);
    page.off('pageerror', onPageError);
  };
}

/** Navigates and resolves once the SPA settled (nav present, network
 * quiet), returning the page geometry -- or null when the route did not
 * load (reported by the caller). `waitUntil` is overridable for pages
 * whose own design keeps the network busy: the Execution page holds an
 * open metric stream, so networkidle never fires there and callers pass
 * 'domcontentloaded' instead (the nav wait + settle pause carry the rest). */
async function settleAt(page, route, { waitUntil = 'networkidle' } = {}) {
  try {
    await page.goto(BASE_URL + route, { waitUntil, timeout: 20000 });
    await page.waitForSelector('nav', { timeout: 10000 });
    // One paint after load, so freshly-mounted cards measure their final
    // geometry rather than the pre-effect skeleton.
    await page.waitForTimeout(300);
    return await page.evaluate(readGeometry);
  } catch (err) {
    check(`${route} loads`, false, err.message.split('\n')[0]);
    return null;
  }
}

/** Geometry the layout invariants are asserted against. */
function readGeometry() {
  const nav = document.querySelector('nav');
  const main = document.querySelector('main');
  const overflowing = [];
  document.querySelectorAll('*').forEach((el) => {
    const box = el.getBoundingClientRect();
    if (box.width > window.innerWidth + 1) {
      overflowing.push(`${el.tagName}.${(el.className || '').toString().trim().split(/\s+/)[0] || '(no class)'}=${Math.round(box.width)}px`);
    }
  });
  return {
    navHeight: nav ? Math.round(nav.getBoundingClientRect().height) : null,
    mainTop: main ? Math.round(main.getBoundingClientRect().top) : null,
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
    overflowing: overflowing.slice(0, 5),
  };
}

/** Console errors that are not mere 4xx resource-load notices. Chromium
 * logs every failed fetch as "Failed to load resource ..." -- but some
 * pages probe optional endpoints BY DESIGN (the Execution page's capacity
 * panel: 404 capacity-profile is the documented "nothing calibrated"
 * contract, and personas without scenario grants see 403s there). Those
 * failures are handled -- the structural assertions prove the page still
 * renders -- so what must stay at zero is everything else: uncaught throws
 * (pageerror) and any other console error. Strict pages (compare, reports)
 * keep asserting on the raw sink. */
function nonResourceErrors(sink) {
  return sink.filter((e) => !/^Failed to load resource/.test(e));
}

/** Drawer state, for the mobile-only open/close assertions. */
function readDrawer() {
  const drawer = document.querySelector('.mobile-menu');
  if (!drawer) {
    return null;
  }
  const style = getComputedStyle(drawer);
  return {
    position: style.position,
    visibility: style.visibility,
    opacity: style.opacity,
    linkCount: drawer.querySelectorAll('a').length,
    hrefs: Array.from(drawer.querySelectorAll('a')).map((a) => a.getAttribute('href')),
    mainTop: Math.round(document.querySelector('main').getBoundingClientRect().top),
  };
}

/** The picker's offered profile ids, in card order. */
function readPickerIds() {
  return Array.from(document.querySelectorAll('[data-testid="profile-card"]'))
    .map((b) => b.querySelector('.font-mono')?.textContent?.trim() ?? '')
    .filter(Boolean);
}

/** Discovers, as the selected persona, the data phase 21's surfaces render:
 * an execution with at least two finalised runs (the compare page needs
 * two), and within it a run whose series and per-label rows both exist
 * (the Reports page's new sections). Purely read-only API walks over the
 * same endpoints the pages use; returns null when this target has no such
 * data -- vite preview has no API, a fresh stack has no finalised runs --
 * so the data-dependent assertions can skip honestly instead of failing. */
async function discoverResultsData(page) {
  try {
    return await page.evaluate(async () => {
      const get = async (url) => {
        const res = await fetch(url, { headers: { accept: 'application/json' } });
        if (!res.ok) {
          return null;
        }
        return res.json();
      };
      const executions = await get('/api/executions');
      if (!Array.isArray(executions) || executions.length === 0) {
        return null;
      }
      // Phase 22 (task 10): the newest execution plus its phase -- the
      // Execution page's live section asserts against it in the persona
      // loop, whatever its state (running -> heading + charts/idle;
      // otherwise an honest skip).
      const live = { id: executions[0].id, phase: null };
      const status = await get(`/api/executions/${live.id}/status`);
      live.phase = status?.phase ?? null;
      for (const exe of executions.slice(0, 5)) {
        const reports = await get(`/api/executions/${exe.id}/reports`);
        if (!Array.isArray(reports) || reports.length < 2) {
          continue;
        }
        const found = { live, executionId: exe.id, runIds: reports.map((r) => r.run_id), runId: null };
        for (const rid of found.runIds) {
          const series = await get(`/api/runs/${rid}/series`);
          const report = await get(`/api/runs/${rid}/report`);
          if (series?.points?.length > 0 && report?.labels?.length > 0) {
            found.runId = rid;
            return found;
          }
        }
        return found; // compare still works; the section assertions skip
      }
      return { live }; // executions exist, but none with two finalised runs
    });
  } catch {
    return null; // no API on this origin, or it is unreachable
  }
}

const executablePath = findChromium();
if (!executablePath) {
  console.error(
    'No Chromium found. Set CHROMIUM_PATH, or provision one with:\n  bunx playwright install chromium'
  );
  process.exit(2);
}

if (WANT_SCREENSHOTS) {
  mkdirSync(SHOT_DIR, { recursive: true });
}

console.log(`layout-check against ${BASE_URL}`);
console.log(`chromium: ${executablePath}\n`);

const browser = await chromium.launch({ executablePath, args: ['--ignore-certificate-errors'] });

try {
  for (const viewport of VIEWPORTS) {
    console.log(`${viewport.name} (${viewport.width}x${viewport.height})`);
    const context = await browser.newContext({
      viewport: { width: viewport.width, height: viewport.height },
      ...(viewport.isMobile ? devices['iPhone 13'] : {}),
      // Re-assert the width: the device descriptor carries its own viewport.
      ...(viewport.isMobile ? { viewport: { width: viewport.width, height: viewport.height } } : {}),
      ignoreHTTPSErrors: true,
    });
    const page = await context.newPage();

    for (const route of ROUTES) {
      const geo = await settleAt(page, route);
      if (!geo) {
        continue;
      }
      // The bug this script exists for: a drawer left in flow inflates the
      // nav and pushes main down by its full height.
      checkLayout(route, geo);

      // Phase 23 (task 7): the skip link is part of the shell, so it must
      // render on every route under the DashboardLayout -- including
      // unauthenticated ones like these (the nav renders, empty).
      const hasSkipLink = await page.evaluate(() => document.querySelector('a[href="#main"]') !== null);
      check(`${route} renders the skip link`, hasSkipLink);

      // Phase 23 (task 7): once per run, on the first route of the first
      // viewport -- Tab from the page body must land on the skip link and
      // it must become visible. Visible-while-focused is the real-engine
      // contract the jsdom suite cannot check (Tailwind resolves the
      // sr-only -> focus:not-sr-only + focus:absolute tie by stylesheet
      // order; if that ever flips, this is what catches it).
      if (!focusCheckDone) {
        focusCheckDone = true;
        await page.keyboard.press('Tab');
        const focused = await page.evaluate(() => {
          const el = document.activeElement;
          const rect = el?.getBoundingClientRect?.() ?? null;
          return {
            href: el?.getAttribute?.('href') ?? null,
            visible: rect !== null && rect.width > 0 && rect.height > 0,
          };
        });
        check('first Tab lands on the skip link', focused.href === '#main', `activeElement href = ${JSON.stringify(focused.href)}`);
        check(
          'skip link becomes visible while focused',
          focused.href === '#main' && focused.visible,
          `href = ${JSON.stringify(focused.href)}, visible = ${focused.visible}`
        );
      }

      if (WANT_SCREENSHOTS) {
        await page.screenshot({
          path: join(SHOT_DIR, `${viewport.name}${route.replace(/\//g, '-')}.png`),
          fullPage: true,
        });
      }
    }

    // Drawer behaviour is mobile-only: at md+ it is `md:hidden` and the
    // inline links take over, so there is no burger button to click.
    if (viewport.isMobile) {
      // /reports while unauthenticated: the nav chrome is there but the
      // permission-filtered nav holds NOTHING -- the picker is what
      // unauthenticated looks like. Wrapped like the ROUTES loop: a down
      // target must fail the run, not crash it behind a stack trace
      // (found when phase 21 first pointed the script at a stopped preview).
      const drawerGoto = await page
        .goto(BASE_URL + '/reports', { waitUntil: 'networkidle', timeout: 20000 })
        .then(() => true)
        .catch((err) => {
          check('/reports loads', false, err.message.split('\n')[0]);
          return false;
        });

      // A missing marker is itself a failure -- an older build, or a refactor
      // that dropped the class the click-outside handler matches on -- but it
      // has to report as one rather than throwing an uncaught timeout that
      // hides every remaining assertion behind a stack trace.
      const hasBurger = drawerGoto
        ? await page.waitForSelector('.mobile-menu-button', { timeout: 10000 }).then(() => true).catch(() => false)
        : false;
      check('burger button carries its .mobile-menu-button marker', hasBurger);

      if (hasBurger) {
        const closed = await page.evaluate(readDrawer);
        // The root invariant. Absolute in BOTH states is what keeps a closed
        // drawer from reserving height; asserting only the open state would
        // have passed against the original bug.
        check('drawer is absolutely positioned while closed', closed?.position === 'absolute', `got ${closed?.position}`);
        check('drawer is hidden while closed', closed?.visibility === 'hidden', `got ${closed?.visibility}`);

        await page.click('.mobile-menu-button');
        await page.waitForTimeout(500);
        const open = await page.evaluate(readDrawer);
        check('drawer becomes visible when opened', open?.visibility === 'visible', `got ${open?.visibility}`);
        check('drawer is still absolutely positioned when open', open?.position === 'absolute', `got ${open?.position}`);
        // Nav visibility is a function of the session: unauthenticated is
        // zero links. The per-persona counts are asserted below, after a
        // persona is actually selected.
        check('unauthenticated drawer holds no links', open?.linkCount === 0, `got ${open?.linkCount} (${(open?.hrefs ?? []).join(', ')})`);
        // Overlay, not push: opening must not move the page underneath.
        check(
          'opening the drawer does not shift main',
          closed != null && open != null && closed.mainTop === open.mainTop,
          `main moved ${closed?.mainTop} -> ${open?.mainTop}`
        );

        if (WANT_SCREENSHOTS) {
          await page.screenshot({ path: join(SHOT_DIR, `${viewport.name}-drawer-open.png`) });
        }

        // Tapping the page outside the drawer closes it -- the only natural
        // dismissal now that the drawer overlays content instead of moving it.
        await page.mouse.click(Math.round(viewport.width / 2), viewport.height - 80);
        await page.waitForTimeout(500);
        const reclosed = await page.evaluate(readDrawer);
        check('tapping outside closes the drawer', reclosed?.visibility === 'hidden', `got ${reclosed?.visibility}`);
      }
    }

    // Per-persona nav counts (task 24): select each demo profile at `/`, then
    // assert the nav each renders matches that persona's permission map --
    // inline links at every width, drawer links at mobile. Needs a demo
    // deployment; a target without one (vite preview with no API, a
    // non-demo deployment) skips honestly instead of failing.
    const pickerLoaded = await page
      .goto(BASE_URL + '/', { waitUntil: 'networkidle', timeout: 20000 })
      .then(() => true)
      .catch((err) => {
        check('/ loads', false, err.message.split('\n')[0]);
        return false;
      });
    let pickerIds = [];
    if (pickerLoaded) {
      try {
        await page.waitForSelector('[data-testid="profile-card"]', { timeout: 5000 });
        pickerIds = await page.evaluate(readPickerIds);
      } catch {
        // No cards: either unauthenticated with demo off, or the API is
        // unreachable and /api/me stayed in its error state.
      }
    }
    if (pickerIds.length === 0) {
      console.log('  skip persona nav checks: no demo profiles offered (demo off, or the API is unreachable)');
    } else {
      for (const persona of PERSONAS) {
        if (!pickerIds.includes(persona.id)) {
          check(`${persona.id} profile is offered`, false, `picker has: ${pickerIds.join(', ')}`);
          continue;
        }
        await page.evaluate((pid) => {
          const card = Array.from(document.querySelectorAll('[data-testid="profile-card"]')).find(
            (b) => b.querySelector('.font-mono')?.textContent?.trim() === pid
          );
          card?.click();
        }, persona.id);
        await page.waitForSelector('[data-testid="demo-banner"]', { timeout: 10000 });

        const inlineHrefs = await page.$$eval('[data-testid="nav-links"] a', (as) => as.map((a) => a.getAttribute('href')));
        check(
          `${persona.id} nav matches the permission map`,
          JSON.stringify(inlineHrefs) === JSON.stringify(persona.hrefs),
          `got ${JSON.stringify(inlineHrefs)}`
        );

        if (viewport.isMobile) {
          await page.click('.mobile-menu-button');
          await page.waitForTimeout(500);
          const open = await page.evaluate(readDrawer);
          check(
            `${persona.id} drawer matches the permission map`,
            JSON.stringify(open?.hrefs) === JSON.stringify(persona.hrefs),
            `got ${JSON.stringify(open?.hrefs)}`
          );
          await page.mouse.click(Math.round(viewport.width / 2), viewport.height - 80);
          await page.waitForTimeout(500);
        }

        // Phase 21 (task 11): /executions/:id/compare renders for every
        // persona that can read executions/reports -- the same grant that
        // shows the Reports nav item, i.e. PERSONAS wholesale (task 10 put
        // the entry link behind can('report','read')). Layout invariants
        // and the heading assert regardless of data; the delta table and
        // p95 overlay need an execution with two finalised runs, which a
        // dataless target cannot offer (honest skip below).
        const results = await discoverResultsData(page);
        const compareId = results?.executionId ?? 1;
        const compareRoute = `/executions/${compareId}/compare`;
        const consoleErrors = [];
        const unwatch = watchConsole(page, consoleErrors);
        const compareGeo = await settleAt(page, compareRoute);
        if (compareGeo) {
          checkLayout(`${persona.id} ${compareRoute}`, compareGeo);
          const h1 = await page.textContent('h1').catch(() => null);
          check(
            `${persona.id} ${compareRoute} renders its heading`,
            (h1 ?? '').includes('Compare runs'),
            `h1 = ${JSON.stringify(h1)}`
          );
          if (results?.runIds && results.runIds.length >= 2) {
            const delta = await page
              .waitForSelector('[data-testid="delta-table"]', { timeout: 10000 })
              .then(() => true)
              .catch(() => false);
            check(`${persona.id} ${compareRoute} renders the delta table`, delta);
            const overlay = await page
              .waitForSelector('[data-testid="chart-compare-p95"]', { timeout: 10000 })
              .then(() => true)
              .catch(() => false);
            check(
              `${persona.id} ${compareRoute} renders the p95 overlay`,
              overlay,
              'both runs need per-second series for the overlay to show'
            );
          } else {
            console.log(`  skip ${persona.id} delta/overlay assertions: no execution with two finalised runs on this target`);
          }
          check(
            `${persona.id} ${compareRoute} logs no console errors`,
            consoleErrors.length === 0,
            consoleErrors.slice(0, 3).join(' | ')
          );
        }
        unwatch();

        // Phase 22 (task 10): the Execution page's live section. Every
        // persona in PERSONAS reads executions (the same grant as the
        // compare surface). The page opens a metric stream for any valid
        // id, so it settles on domcontentloaded (see settleAt). The live
        // section renders only while the execution runs (or once series
        // arrived); a finished, eventless execution shows neither honestly,
        // so the live assertions are phase-conditional and skip otherwise.
        if (results?.live) {
          const liveRoute = `/executions/${results.live.id}`;
          const liveErrors = [];
          const unwatchLive = watchConsole(page, liveErrors);
          const liveGeo = await settleAt(page, liveRoute, { waitUntil: 'domcontentloaded' });
          if (liveGeo) {
            checkLayout(`${persona.id} ${liveRoute}`, liveGeo);
            const h1 = await page.textContent('h1').catch(() => null);
            check(
              `${persona.id} ${liveRoute} renders its heading`,
              (h1 ?? '').includes('Execution'),
              `h1 = ${JSON.stringify(h1)}`
            );
            if (results.live.phase === 'running') {
              const section = await page
                .waitForSelector('[data-testid="live-section"]', { timeout: 10000 })
                .then(() => true)
                .catch(() => false);
              check(`${persona.id} ${liveRoute} renders the live section while running`, section);
              const heading = await page
                .textContent('[data-testid="live-section"] h3')
                .catch(() => null);
              check(
                `${persona.id} ${liveRoute} live section carries its "Live" heading`,
                (heading ?? '').trim() === 'Live',
                `heading = ${JSON.stringify(heading)}`
              );
              // Which of the two depends on whether events arrived yet:
              // one comma-selector waits for either.
              const idleOrChart = await page
                .waitForSelector('[data-testid="live-idle"], [data-testid="live-chart-vus-rps"]', { timeout: 10000 })
                .then(() => true)
                .catch(() => false);
              check(
                `${persona.id} ${liveRoute} shows live charts or the idle placeholder`,
                idleOrChart,
                'neither the idle line nor the first chart rendered'
              );
            } else {
              console.log(
                `  skip ${persona.id} live-section assertions: execution ${results.live.id} is not running (phase ${JSON.stringify(results.live.phase)})`
              );
            }
            // Phase 23 (task 7): the Execution page's copy-link button sits
            // by the h1 for every session that can read the page; asserted
            // for alice per the task's scope (her map reads everything, so
            // the button's presence cannot be a permissions artifact).
            if (persona.id === 'alice') {
              const copyLink = await page
                .waitForSelector('[data-testid="copy-link"]', { timeout: 10000 })
                .then(() => true)
                .catch(() => false);
              check(`${persona.id} ${liveRoute} renders the copy-link button`, copyLink);

              // Phase 24 (task 8): the action-error alert block. Rendering
              // the details envelope inside it is unit-tested; the layout
              // concern is that a FAILED lifecycle action puts a visible
              // role=alert into the DOM at real viewport widths -- jsdom
              // cannot see it, and a block that renders detached or zero-
              // sized would surface here as a missing/invisible node. The
              // failure is manufactured entirely client-side: the lifecycle
              // POST is intercepted and answered with a stubbed 409 before
              // it can leave the browser, so this stays read-only against
              // any target (this script reads deployments, it never builds
              // or stops them). Whichever lifecycle button the phase leaves
              // enabled serves as the click.
              const lifecyclePOST = /\/api\/executions\/\d+\/(deploy|trigger|stop|purge)$/;
              const stubConflict = (route) =>
                route.fulfill({
                  status: 409,
                  contentType: 'application/json',
                  body: JSON.stringify({ message: 'layout-check stubbed conflict' }),
                });
              await page.route(lifecyclePOST, stubConflict);
              const actionButton = page
                .locator('button:enabled', { hasText: /^(Deploy|Trigger|Stop|Purge)$/ })
                .first();
              if ((await actionButton.count()) === 0) {
                console.log(`  skip ${persona.id} action-error assertion: no enabled lifecycle button for phase ${JSON.stringify(results.live.phase)}`);
              } else {
                await actionButton.click();
                // Several role=alert nodes can coexist on this page (the
                // embedded TaurusEditor carries its own error alert, e.g.
                // the 409 a native scenario's fragment fetch answers with)
                // and one is present from load -- so waiting for "an alert"
                // alone races the stubbed round trip. Wait for THE alert:
                // only the action-error block can carry the stub's text.
                const ok = await page
                  .waitForFunction(
                    () =>
                      Array.from(document.querySelectorAll('p[role="alert"]')).some((p) =>
                        (p.textContent ?? '').includes('layout-check stubbed conflict')
                      ),
                    { timeout: 10000 }
                  )
                  .then(() => true)
                  .catch(() => false);
                const alertTexts = ok
                  ? null
                  : await page
                      .$$eval('p[role="alert"]', (ps) => ps.map((p) => (p.textContent ?? '').trim()))
                      .catch(() => []);
                check(`${persona.id} ${liveRoute} surfaces the action-error alert block`, ok, `alert texts = ${JSON.stringify(alertTexts)}`);
              }
              await page.unroute(lifecyclePOST, stubConflict);
            }
            check(
              `${persona.id} ${liveRoute} logs no console errors beyond by-design 4xx probes`,
              nonResourceErrors(liveErrors).length === 0,
              nonResourceErrors(liveErrors).slice(0, 3).join(' | ')
            );
          }
          unwatchLive();
        } else {
          console.log(`  skip ${persona.id} live-section assertions: no execution on this target`);
        }

        // Phase 23 (task 7): the report page's export + deep-link affordances,
        // asserted for the admin persona: Export CSV / Export JSON anchors and
        // the Copy link button. They render with the page itself, so the
        // honest skip is the page's own: no discovered run on this target.
        if (persona.id === 'alice') {
          if (results?.runId != null) {
            const exportRoute = `/reports/${results.runId}`;
            const exportErrors = [];
            const unwatchExport = watchConsole(page, exportErrors);
            const exportGeo = await settleAt(page, exportRoute);
            if (exportGeo) {
              checkLayout(`alice ${exportRoute} (export affordances)`, exportGeo);
              for (const id of ['export-csv', 'export-json', 'export-pdf', 'copy-link']) {
                const present = await page
                  .waitForSelector(`[data-testid="${id}"]`, { timeout: 10000 })
                  .then(() => true)
                  .catch(() => false);
                check(`alice ${exportRoute} renders ${id}`, present);
              }
              check(
                `alice ${exportRoute} logs no console errors`,
                exportErrors.length === 0,
                exportErrors.slice(0, 3).join(' | ')
              );
            }
            unwatchExport();
          } else {
            console.log('  skip alice export/copy-link assertions: no run with series + labels on this target');
          }
        }

        // Phase 22 (task 10): the NewTest page's stage editor. The editor
        // renders for every persona -- only the create button is gated by
        // execution:create -- so the table's header contract (the columns
        // the phase named) asserts for all four.
        {
          const newErrors = [];
          const unwatchNew = watchConsole(page, newErrors);
          const newGeo = await settleAt(page, '/executions/new');
          if (newGeo) {
            checkLayout(`${persona.id} /executions/new`, newGeo);
            const headers = await page.$$eval('[data-testid="stage-table"] thead th', (ths) =>
              ths.map((t) => t.textContent?.trim() ?? '')
            );
            for (const col of ['Concurrency', 'Duration']) {
              check(
                `${persona.id} /executions/new keeps its "${col}" stage column`,
                headers.some((h) => h.startsWith(col)),
                `headers: ${headers.join(', ')}`
              );
            }
            check(
              `${persona.id} /executions/new logs no console errors`,
              newErrors.length === 0,
              newErrors.slice(0, 3).join(' | ')
            );
          }
          unwatchNew();
        }

        // Phase 22 (task 10): the Clusters page's capacity column. alice
        // is the only persona whose permission map grants /clusters
        // (system:admin); the others' maps already assert they never even
        // see the nav item. The table -- and its Capacity header -- exists
        // only when the registry holds entries; an empty registry renders
        // the honest empty state and the header check skips.
        if (persona.hrefs.includes('/clusters')) {
          const clusterErrors = [];
          const unwatchClusters = watchConsole(page, clusterErrors);
          const clusterGeo = await settleAt(page, '/clusters');
          if (clusterGeo) {
            checkLayout(`${persona.id} /clusters`, clusterGeo);
            const headers = await page.$$eval('table thead th', (ths) => ths.map((t) => t.textContent?.trim() ?? ''));
            if (headers.length > 0) {
              check(
                `${persona.id} /clusters keeps its Capacity column`,
                headers.includes('Capacity'),
                `headers: ${headers.join(', ')}`
              );
              // Phase 25 (task 4): when the quota ledger is wired and any
              // row carries capacity numbers, the meter must render the
              // used/ceiling figures (e.g. "2 / 12") rather than the honest
              // no-data line. Rows without numbers (unconfigured cluster)
              // legitimately keep the no-data state, so this asserts on the
              // numbers' presence-or-absence matching the meter text, and
              // only reports rows that show neither (a broken mapping).
              const meterTexts = await page.$$eval('table tbody tr td:last-child span, table tbody tr td:last-child p', (els) =>
                els.map((e) => (e.textContent ?? '').trim()).filter(Boolean)
              );
              const anyNumbers = meterTexts.some((t) => /\d+\s*\/\s*\d+/.test(t));
              const anyNoData = meterTexts.some((t) => /no capacity/i.test(t));
              check(
                `${persona.id} /clusters meters render numbers or the honest no-data state`,
                anyNumbers || anyNoData,
                meterTexts.length === 0
                  ? 'no meter elements found (empty registry)'
                  : `meters: ${meterTexts.slice(0, 3).join(' | ')}`
              );
            } else {
              console.log(
                '  skip capacity-column assertion: no table to carry the header (empty registry, or registry endpoints not deployed -- they need the k8s scheduler)'
              );
            }
            check(
              `${persona.id} /clusters logs no console errors beyond by-design 4xx probes`,
              nonResourceErrors(clusterErrors).length === 0,
              nonResourceErrors(clusterErrors).slice(0, 3).join(' | ')
            );
          }
          unwatchClusters();
        }

        // The Reports page's new sections, once for the viewer persona at
        // every width: the time-series card, its requested-vs-achieved
        // overlay, and the per-label table only exist when a run actually
        // carries series and labels, so this too needs seeded data. carol
        // is the least-privileged reader; if it renders for her it renders
        // for the rest.
        if (persona.id === SECTION_PERSONA) {
          if (results?.runId != null) {
            const reportRoute = `/reports/${results.runId}`;
            const sectionErrors = [];
            const unwatchSections = watchConsole(page, sectionErrors);
            const reportGeo = await settleAt(page, reportRoute);
            if (reportGeo) {
              checkLayout(reportRoute, reportGeo);
              const sections = await page.$$eval('h3', (hs) => hs.map((h) => h.textContent?.trim() ?? ''));
              for (const title of ['Time series', 'Requested vs achieved', 'Per-label results']) {
                check(
                  `${reportRoute} renders the "${title}" section`,
                  sections.includes(title),
                  `h3 titles: ${sections.join(', ')}`
                );
              }
              for (const testId of ['chart-vus-rps', 'chart-errors', 'chart-latency', 'chart-requested', 'labels-table']) {
                const present = await page
                  .waitForSelector(`[data-testid="${testId}"]`, { timeout: 10000 })
                  .then(() => true)
                  .catch(() => false);
                check(`${reportRoute} renders ${testId}`, present);
              }
              check(
                `${reportRoute} logs no console errors`,
                sectionErrors.length === 0,
                sectionErrors.slice(0, 3).join(' | ')
              );
            }
            unwatchSections();
          } else {
            console.log('  skip report-section assertions: no run with series + labels on this target');
          }
        }

        // Logout returns to the picker so the next persona can be selected.
        await page.click('[data-testid="demo-banner"] button');
        await page.waitForSelector('[data-testid="profile-card"]', { timeout: 10000 });
      }
      const uncovered = pickerIds.filter((pid) => !PERSONAS.some((p) => p.id === pid));
      for (const extra of uncovered) {
        console.log(`  note: profile "${extra}" has no expected-nav entry; not asserted`);
      }
    }

    await context.close();
    console.log('');
  }
} finally {
  await browser.close();
}

if (failures.length > 0) {
  console.error(`layout-check FAILED (${failures.length}):`);
  failures.forEach((f) => console.error(`  - ${f}`));
  process.exit(1);
}
console.log('layout-check passed');
