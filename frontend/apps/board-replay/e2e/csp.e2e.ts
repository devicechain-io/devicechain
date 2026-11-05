// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The strict-CSP gate: the built replay bundle (dist/) must run, draw the board and its charts,
// and play, on a page whose policy says `style-src 'self'` -- no 'unsafe-inline', so no <style>
// element and no style attribute set through markup. A host such as the project website that
// serves the bundle under that policy would otherwise get an unstyled page and a console full of
// violations.
//
// The assertion is zero `securitypolicyviolation` events, collected from before any script runs,
// PLUS a board that actually drew (a replay that blocked its own stylesheet still "has no
// errors"). The first test is the detector's negative control: the same policy with a <style>
// element injected must report a violation, so a zero in the second test means something.

import { expect, test, type Page } from '@playwright/test';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import path from 'node:path';

const APP = path.resolve(import.meta.dirname, '..');
const DIST = path.join(APP, 'dist');
const FRONTEND = path.resolve(APP, '../..');

const STRICT_CSP = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' data: blob:",
  "worker-src 'self' blob:",
  "connect-src 'self'",
].join('; ');

const BOARD = readFileSync(path.join(FRONTEND, 'testdata/sim-dashboards/tp-dashboard.json'));
const RECORDING = (() => {
  const doc = JSON.parse(readFileSync(path.join(FRONTEND, 'testdata/board-replay/towerpulse-fixture.json'), 'utf-8'));
  // The fixture pins a placeholder board hash; point it at the board on disk, as the unit suites do.
  doc.board = { ...doc.board, sha256: createHash('sha256').update(BOARD).digest('hex') };
  return JSON.stringify(doc);
})();

const TYPES: Record<string, string> = { '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.wasm': 'application/wasm' };

// A host page like the website's: one mount element, one module script, nothing inline.
const HOST = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>host</title></head>
<body><div id="board-replay" data-recording="/data/recording.json" data-board="/data/board.json"></div>
<script type="module" src="/dist/main.js"></script></body></html>`;

let server: Server;
let origin = '';

test.beforeAll(async () => {
  if (!existsSync(path.join(DIST, 'main.js'))) throw new Error('dist/main.js is missing: run `npm run test:csp`');
  server = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://x').pathname;
    const send = (body: string | Buffer, type: string) => {
      res.setHeader('Content-Type', type);
      res.setHeader('Content-Security-Policy', STRICT_CSP);
      res.end(body);
    };
    if (url === '/') return send(HOST, 'text/html');
    // The negative control's page: the same policy, plus the one thing the policy forbids.
    if (url === '/control') return send('<!doctype html><title>c</title><body><script src="/control.js"></script>', 'text/html');
    if (url === '/control.js') return send("document.head.appendChild(Object.assign(document.createElement('style'), { textContent: 'body{color:red}' }));", 'text/javascript');
    if (url === '/data/recording.json') return send(RECORDING, 'application/json');
    if (url === '/data/board.json') return send(BOARD, 'application/json');
    if (url.startsWith('/dist/')) {
      const file = path.join(DIST, path.normalize(url.slice('/dist/'.length)));
      if (file.startsWith(DIST) && existsSync(file)) return send(readFileSync(file), TYPES[path.extname(file)] ?? 'application/octet-stream');
    }
    res.statusCode = 404;
    res.end();
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

test.afterAll(() => {
  server.close();
});

type Spy = { __csp: string[] };

async function watchViolations(page: Page) {
  await page.addInitScript(() => {
    (window as unknown as Spy).__csp = [];
    document.addEventListener('securitypolicyviolation', (e) => {
      (window as unknown as Spy).__csp.push(`${e.violatedDirective} blocked ${e.blockedURI || 'inline'} :: ${e.sample}`);
    });
  });
}

const violations = (page: Page) => page.evaluate(() => (window as unknown as Spy).__csp);

test('negative control: the policy reports an injected <style> element', async ({ page }) => {
  await watchViolations(page);
  await page.goto(`${origin}/control`);
  await expect.poll(() => violations(page)).toHaveLength(1);
});

test('the replay bundle draws and plays under style-src self with zero violations', async ({ page }) => {
  const consoleErrors: string[] = [];
  page.on('console', (m) => m.type() === 'error' && consoleErrors.push(m.text()));
  page.on('pageerror', (e) => consoleErrors.push(String(e)));
  await watchViolations(page);
  await page.goto(`${origin}/`);

  await expect(page.getByTestId('transport-bar')).toBeVisible({ timeout: 30_000 });
  // The board drew: widgets are placed, and the theme took effect (it is a stylesheet, so a
  // blocked one leaves the root with a transparent background).
  await expect(page.locator('[data-widget]').first()).toBeVisible();
  expect(await page.locator('[data-widget]').count()).toBeGreaterThan(1);
  const bg = await page.getByTestId('replay-root').evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(bg, 'the theme stylesheet did not apply').not.toBe('rgba(0, 0, 0, 0)');
  // Chart widgets are canvases drawn by ECharts, whose container is styled through CSSOM.
  await expect(page.locator('canvas').first()).toBeVisible({ timeout: 30_000 });

  // Let it play, and sweep the pointer across every chart: a tooltip is built only on interaction
  // (an HTML one carries style attributes in its markup, which this policy refuses).
  const bar = page.getByTestId('transport-bar');
  await bar.locator('[role=group] button').last().click(); // the fastest speed
  await bar.locator('button').first().click(); // Play
  await expect(page.getByTestId('replay-state')).toHaveText(/play/i);
  await page.waitForTimeout(4000);
  const canvases = page.locator('canvas');
  for (let i = 0; i < (await canvases.count()); i++) {
    await canvases.nth(i).scrollIntoViewIfNeeded();
    const box = await canvases.nth(i).boundingBox();
    if (!box || box.width < 20 || box.height < 20) continue;
    for (const fx of [0.2, 0.4, 0.6, 0.8]) {
      await page.mouse.move(box.x + box.width * fx, box.y + box.height * 0.5, { steps: 4 });
      await page.waitForTimeout(80);
    }
  }

  expect(await violations(page)).toEqual([]);
  expect(consoleErrors.filter((m) => /content security policy|refused to apply/i.test(m))).toEqual([]);
});
