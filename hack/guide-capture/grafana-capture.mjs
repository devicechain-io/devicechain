// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Screenshots of an instance's Grafana boards, driven by hack/grafana-shots.sh.
//
//   node grafana-capture.mjs <shots.json>
//
// shots.json: { "grafana": "http://localhost:23000", "instance": "demo", "outDir": "...",
//               "width": 1600, "height": 1000,
//               "shots": [ { "name": "overview", "board": "overview", "from": "now-30m",
//                            "to": "now", "viewPanel": 12, "scrollTo": "<row title>" } ] }
//
// The Grafana admin password is read from GRAFANA_PASSWORD and never written anywhere: it goes
// into the browser's HTTP basic-auth credentials, and no trace, video or HAR is recorded.
//
// A board's uid is computed, not looked up: the chart names every board
// "dc-" + the first 20 hex characters of sha256("<instance>/<file stem>")
// (deploy/helm/devicechain/templates/grafana-dashboard.yaml). The page must then show that
// board's title with "[<instance>]" appended, or the shot fails: a 404 page or a login form
// photographed at 1600x1000 looks like a picture too.

import { createHash } from 'node:crypto';
import { readFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { chromium } from '@playwright/test';
import sharp from 'sharp';

const spec = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const password = process.env.GRAFANA_PASSWORD;
if (!password) throw new Error('GRAFANA_PASSWORD is not set');

const width = spec.width ?? 1600;
const height = spec.height ?? 1000;
const tall = spec.tallHeight ?? 4000; // the page height a scrollTo shot is cropped from
mkdirSync(spec.outDir, { recursive: true });

const uid = (stem) =>
  'dc-' + createHash('sha256').update(`${spec.instance}/${stem}`).digest('hex').slice(0, 20);

const browser = await chromium.launch({ channel: 'chrome' });
const context = await browser.newContext({
  viewport: { width, height },
  deviceScaleFactor: 1,
  timezoneId: 'UTC',
  locale: 'en-US',
  colorScheme: 'dark',
  reducedMotion: 'reduce',
});
const page = await context.newPage();

// Grafana's UI redirects to its login form rather than challenging, so basic auth alone never
// reaches the board: sign in once through the login endpoint for a session cookie.
const login = await context.request.post(`${spec.grafana}/login`, {
  data: { user: 'admin', password },
});
if (!login.ok()) throw new Error(`Grafana sign-in failed: HTTP ${login.status()}`);

let failed = 0;
for (const shot of spec.shots) {
  const q = new URLSearchParams({
    orgId: '1',
    from: shot.from ?? 'now-30m',
    to: shot.to ?? 'now',
    timezone: 'utc',
    theme: 'dark',
    refresh: '',
  });
  if (shot.viewPanel !== undefined) q.set('viewPanel', String(shot.viewPanel));
  const url = `${spec.grafana}/d/${uid(shot.board)}?${q.toString()}&kiosk`;
  await page.setViewportSize({ width, height: shot.scrollTo ? tall : height });
  await page.goto(url, { waitUntil: 'networkidle', timeout: 90_000 });

  // The board must be the one asked for, on this instance. Grafana sets the title once the
  // board's model has loaded.
  const want = `[${spec.instance}]`;
  await page.waitForFunction((w) => document.title.includes(w), want, { timeout: 60_000 }).catch(() => {});
  const title = await page.title();
  if (!title.includes(want)) {
    console.error(`FAIL ${shot.name}: page title "${title}" is not a board of ${spec.instance} (${page.url()})`);
    failed++;
    continue;
  }
  // Panels query as they come into view; give the slowest query time to land, then require
  // that no panel is still loading.
  await page.waitForTimeout(shot.settle ?? 8000);
  await page
    .waitForFunction(() => document.querySelectorAll('[aria-label="Panel loading bar"]').length === 0, null, {
      timeout: 60_000,
    })
    .catch(() => console.error(`WARN ${shot.name}: a panel was still loading after 60s`));

  // A shot may open lower down the board, at a row of its own choosing. Scrolling Grafana's page
  // leaves panels clipped at the old fold, so such a shot is taken from a page tall enough to hold
  // the whole board, cropped to the frame with the row's header at its top.
  let clip;
  if (shot.scrollTo) {
    const box = await page.getByText(shot.scrollTo, { exact: true }).first().boundingBox();
    if (!box) {
      console.error(`FAIL ${shot.name}: no row "${shot.scrollTo}" on the board`);
      failed++;
      continue;
    }
    clip = { x: 0, y: Math.max(0, Math.min(box.y - 12, tall - height)), width, height };
  }
  const png = await page.screenshot({ type: 'png', clip });
  const out = join(spec.outDir, `${shot.name}.webp`);
  const info = await sharp(png).webp({ quality: 82, effort: 6 }).toFile(out);
  console.log(`shot ${shot.name}: ${title} -> ${out} (${Math.round(info.size / 1024)} KiB)`);
}

await browser.close();
if (failed) {
  console.error(`${failed} shot(s) failed`);
  process.exit(1);
}
