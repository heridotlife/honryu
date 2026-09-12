#!/usr/bin/env node
/**
 * Honryu operator-SPA e2e boot harness -- a PERMANENT MANUAL gate.
 *
 * Why this exists: the phase49 redirect-loop regression shipped because
 * nothing exercised the UNAUTHENTICATED first paint. Vitest runs in jsdom,
 * which computes no real navigation; this harness drives real Chromium
 * against a live deployment and asserts the boot contract end to end:
 *
 *   A. UNAUTHENTICATED BOOT  / renders the profile picker, no reload loop,
 *                            zero pageerror.
 *   B. LOGIN                 picking a persona SPA-navigates away from /.
 *   C. SESSION DEATH         clearing cookies mid-session lands back on /
 *                            (the once-latch dead-session redirect).
 *   D. PURGE TWO-STEP        the destructive Purge control arms before it
 *                            fires, and fires exactly one request.
 *   E. ROW SANITY            no raw ISO timestamp leaks into a list row.
 *   F. MOBILE NO OVERFLOW    at a 375px viewport the page must not scroll
 *                            sideways (phase 52's 5px-overflow audit fix).
 *
 * It is NOT wired into `bun run test` or CI: it needs a live environment
 * (a running API + SPA, demo auth enabled, reachable engines for D). Run it
 * by hand before operator-facing releases:
 *
 *   cd web && bun run e2e
 *   HONRYU_E2E_BASE_URL=https://other-host bun run e2e
 *
 * Exit code is non-zero if any scenario FAILs; SKIP (recorded when demo
 * drift makes a scenario not applicable, e.g. no purgeable execution)
 * still passes the run.
 */
import { existsSync, readdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright-core';

const BASE_URL = process.env.HONRYU_E2E_BASE_URL || 'https://honryu.pve.heri.life';

/** Chromium discovery, mirroring scripts/layout-check.js: the Playwright
 * browser cache on this host, skipping the headless-shell builds. */
function findChromium() {
  if (process.env.CHROMIUM_PATH) return process.env.CHROMIUM_PATH;
  const cache = join(homedir(), '.cache', 'ms-playwright');
  if (!existsSync(cache)) return null;
  const dir = readdirSync(cache).find((d) => d.startsWith('chromium-') && !d.includes('shell'));
  if (!dir) return null;
  // Layouts differ across playwright versions and platforms.
  for (const rel of ['chrome-linux64/chrome', 'chrome-linux/chrome', 'chrome-mac/Chromium.app/Contents/MacOS/Chromium']) {
    const full = join(cache, dir, rel);
    if (existsSync(full)) return full;
  }
  return null;
}

const results = [];
function record(status, name, detail) {
  console.log(`${status.padEnd(4)} ${name}${detail ? ` -- ${detail}` : ''}`);
  results.push(status);
}
const pass = (name, detail) => record('PASS', name, detail);
const fail = (name, detail) => record('FAIL', name, detail);
const skip = (name, detail) => record('SKIP', name, detail);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * Count REAL document navigations of the main frame. `framenavigated` also
 * fires for same-document (history API) navigations -- and SPA route changes
 * must not count (the loop bug this guards against produced endless reloads,
 * i.e. endless new documents). Classification: an init script stamps every
 * fresh document with a nonce; a framenavigated event counts only when the
 * main frame now carries a nonce we have not seen. Same document => same
 * nonce => not counted.
 */
async function attachDocumentNavCounter(page) {
  await page.addInitScript(() => {
    window.__honryuE2eDocNonce = `${Date.now()}-${Math.random()}`;
  });
  const t = { count: 0, urls: [] };
  let lastNonce = null;
  const classify = async (frame) => {
    // A document just committed; the fresh execution context (and its nonce)
    // may need a beat. Retry instead of misattributing the event.
    for (let attempt = 0; attempt < 10; attempt++) {
      try {
        const nonce = await frame.evaluate(() => window.__honryuE2eDocNonce ?? null);
        if (nonce === null) {
          await sleep(100);
          continue;
        }
        if (nonce !== lastNonce) {
          lastNonce = nonce;
          t.count += 1;
          t.urls.push(frame.url());
        }
        return;
      } catch {
        await sleep(100);
      }
    }
  };
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) void classify(frame);
  });
  return t;
}

/** Scenario A precondition, reused by B/C/D/E: the picker rendered, and the
 * "alice" persona found. Returns the persona button locator. */
async function openPicker(page) {
  await page.goto(`${BASE_URL}/`, { waitUntil: 'domcontentloaded' });
  const h1 = page.locator('h1');
  await h1.waitFor({ state: 'visible', timeout: 10_000 });
  const text = (await h1.textContent()) ?? '';
  if (!text.includes("Who's operating?")) {
    throw new Error(`picker h1 = ${JSON.stringify(text)}, want "Who's operating?"`);
  }
  const alice = page.locator('[data-testid="profile-card"]', { hasText: 'alice' }).first();
  if ((await alice.count()) === 0) {
    const names = await page.locator('[data-testid="profile-card"]').allTextContents();
    throw new Error(`no "alice" persona card; cards: ${JSON.stringify(names)}`);
  }
  return alice;
}

/** Pick the alice persona and wait for the SPA to settle on a route. */
async function login(page, alice) {
  await alice.click();
  await page.waitForFunction(() => location.pathname !== '/', undefined, { timeout: 15_000 });
  await sleep(2_000);
}

async function scenarioABandC(browser) {
  // A, B, C share one page: they are one continuous visitor lifetime.
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const navs = await attachDocumentNavCounter(page);
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  try {
    // A. UNAUTHENTICATED BOOT: picker renders, no reload loop, no pageerror.
    const alice = await openPicker(page);
    await sleep(3_000); // a reload loop would show itself within this window
    if (navs.count !== 1) {
      fail('A. unauthenticated boot', `expected exactly 1 main-frame document navigation since load, got ${navs.count}: ${JSON.stringify(navs.urls)}`);
    } else {
      pass('A. unauthenticated boot', 'picker rendered, exactly 1 document navigation in 3s');
    }
    if (pageErrors.length !== 0) {
      fail('A. zero pageerror on boot', JSON.stringify(pageErrors));
    } else {
      pass('A. zero pageerror on boot');
    }

    // B. LOGIN: persona click SPA-navigates away from / without a reload.
    await login(page, alice);
    const path = new URL(page.url()).pathname;
    if (path === '/') {
      fail('B. login', `URL still ${page.url()} after persona click`);
    } else if (navs.count !== 1) {
      fail('B. login', `SPA nav should not reload the document; navigations: ${JSON.stringify(navs.urls)}`);
    } else {
      pass('B. login', `SPA-navigated to ${path}, document navigations still 1`);
    }

    // C. SESSION DEATH: kill the cookie server-side-of-the-browser, click a
    // nav link, and the once-latch dead-session redirect must land on /.
    await page.context().clearCookies();
    await page.locator('[data-testid="nav-links"] a', { hasText: 'Executions' }).click();
    await sleep(3_000);
    const finalPath = new URL(page.url()).pathname;
    if (finalPath !== '/') {
      fail('C. session death redirect', `expected final URL /, got ${page.url()}`);
    } else {
      pass('C. session death redirect', `dead session landed back on / (navigations: ${JSON.stringify(navs.urls)})`);
    }
  } catch (err) {
    fail('A/B/C boot-login-session flow', String(err));
  } finally {
    await ctx.close();
  }
}

async function scenarioDandE(browser) {
  // D, E need a signed-in visitor with a clean cookie jar.
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  let purgeRequests = 0;
  page.on('request', (req) => {
    if (/\/purge(\?|$)/.test(req.url())) purgeRequests += 1;
  });

  try {
    const alice = await openPicker(page);
    await login(page, alice);
    await page.goto(`${BASE_URL}/executions`, { waitUntil: 'domcontentloaded' });

    // E. ROW SANITY runs on the list itself, before D leaves it.
    // Rows link to /executions/{id}; the page's own "New test" CTA links to
    // /executions/new and must not be mistaken for a row.
    const rowSelector = 'main a[href^="/executions/"]:not([href="/executions/new"])';
    await page.waitForSelector(`${rowSelector}, main p`, { timeout: 15_000 });
    const rows = await page.locator(rowSelector).all();
    const isoLeak = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/;
    if (rows.length === 0) {
      pass('E. row sanity', 'no executions listed -- vacuously clean');
    } else {
      const texts = await page.locator(rowSelector).allTextContents();
      const leaking = texts.filter((t) => isoLeak.test(t));
      if (leaking.length !== 0) {
        fail('E. row sanity', `raw ISO timestamp in ${leaking.length}/${texts.length} rows: ${JSON.stringify(leaking)}`);
      } else {
        pass('E. row sanity', `${texts.length} rows, no raw ISO timestamp`);
      }
    }

    // D. PURGE TWO-STEP on any execution detail.
    if (rows.length === 0) {
      skip('D. purge two-step', 'no executions on the hub to open');
      return;
    }
    await page.locator(rowSelector).first().click();
    await page.waitForURL(/\/executions\/\d+/, { timeout: 15_000 });
    await page.waitForLoadState('domcontentloaded');

    const purge = page.getByRole('button', { name: 'Purge', exact: true });
    if ((await purge.count()) === 0) {
      skip('D. purge two-step', 'Purge control not offered for this execution (phase/permission gated)');
      return;
    }
    if (await purge.isDisabled()) {
      skip('D. purge two-step', 'Purge disabled (nothing deployed -- demo drift)');
      return;
    }
    await purge.click();
    const armed = page.getByRole('button', { name: 'Confirm purge?' });
    await armed.waitFor({ state: 'visible', timeout: 5_000 });
    await sleep(1_000); // any premature fire would have hit the wire by now
    if (purgeRequests !== 0) {
      fail('D. purge arms before firing', `${purgeRequests} /purge request(s) fired on arm click`);
    } else {
      pass('D. purge arms before firing', 'armed control visible, zero /purge requests');
    }
    await armed.click();
    for (let i = 0; i < 20 && purgeRequests === 0; i++) await sleep(250);
    await sleep(1_500); // settle: a double fire would show here
    if (purgeRequests !== 1) {
      fail('D. purge fires exactly once', `${purgeRequests} /purge request(s) after confirm`);
    } else {
      pass('D. purge fires exactly once', 'one /purge request after confirm');
    }
  } catch (err) {
    fail('D/E purge + row sanity flow', String(err));
  } finally {
    await ctx.close();
  }
}

async function scenarioF(browser) {
  // F owns a NARROW context: the audit measured ~5px of sideways scroll on
  // every route at 375px (380 vs 375). The fix lives in the nav's flex
  // chain (gap instead of phantom space-x margins, min-w-0 down to the
  // switcher's truncating button), so the check drives both an
  // unauthenticated and an authenticated page -- the CTA widens the nav's
  // right group, which is exactly where the squeeze must be absorbed.
  const ctx = await browser.newContext({ viewport: { width: 375, height: 667 } });
  const page = await ctx.newPage();
  try {
    const alice = await openPicker(page);
    const noOverflow = () =>
      page.evaluate(() => ({
        body: document.body.scrollWidth,
        doc: document.documentElement.scrollWidth,
        vw: document.documentElement.clientWidth,
      }));
    let m = await noOverflow();
    if (m.body > m.vw || m.doc > m.vw) {
      fail('F. mobile no horizontal overflow', `unauth /: body=${m.body} doc=${m.doc} > vw=${m.vw}`);
    } else {
      pass('F. mobile no horizontal overflow', `unauth /: body=${m.body} doc=${m.doc} <= vw=${m.vw}`);
    }

    await login(page, alice);
    await page.goto(`${BASE_URL}/executions`, { waitUntil: 'domcontentloaded' });
    await sleep(2_000); // let the list and the nav's switcher settle
    m = await noOverflow();
    if (m.body > m.vw || m.doc > m.vw) {
      fail('F. mobile no horizontal overflow (authed)', `/executions: body=${m.body} doc=${m.doc} > vw=${m.vw}`);
    } else {
      pass('F. mobile no horizontal overflow (authed)', `/executions: body=${m.body} doc=${m.doc} <= vw=${m.vw}`);
    }
  } catch (err) {
    fail('F. mobile no horizontal overflow', String(err));
  } finally {
    await ctx.close();
  }
}

const executablePath = findChromium();
if (!executablePath) {
  console.error('No Chromium found. Set CHROMIUM_PATH, or provision one with: bunx playwright install chromium');
  process.exit(1);
}
console.log(`honryu e2e boot harness -- ${BASE_URL}`);
console.log(`chromium: ${executablePath}\n`);

const browser = await chromium.launch({ executablePath, args: ['--no-sandbox'] });
try {
  await scenarioABandC(browser);
  await scenarioDandE(browser);
  await scenarioF(browser);
} finally {
  await browser.close();
}

const failed = results.filter((r) => r === 'FAIL').length;
console.log(`\n${results.length} checks: ${results.filter((r) => r === 'PASS').length} pass, ${failed} fail, ${results.filter((r) => r === 'SKIP').length} skip`);
process.exit(failed > 0 ? 1 : 0);
