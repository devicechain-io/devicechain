// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The instrument. Serves one arm's build output, drives a real headless Chrome at it,
// and asserts what the map ACTUALLY did.
//
// 🔴 WHY A BROWSER AND NOT A BUILD CHECK. This arc's defect is a bundler-emitted worker
// URL that resolves to nothing useful: the build is green, the types are green, the
// tests are green, and the map renders nothing. Neither the artifact nor the build log
// contains the value — it is computed at runtime by code the bundler wrote. So the only
// place the claim can be tested is a browser that has run it. Measured, both ways: the
// two real defects this rig has caught so far compiled with EXIT CODE 0.
//
// 🔴 WHY THE SERVER IS PART OF THE INSTRUMENT. Tiles and the worker are both fetched
// from this process, so "the page asked for the worker and got 200" and "the page asked
// for tiles and got 200" are recorded HERE, on the serving side, rather than inferred
// from the page.
//
// 🔴 WHY PIXELS. Fetching the worker is not the same as the worker WORKING, and neither
// the DOM nor the console can tell the difference. Measured: with a worker whose own
// sibling import 404s, this page rendered two canvases, eight markers, ten tiles, no
// fallback panel, no notice and ZERO CONSOLE ERRORS. The bundled basemap's land polygons
// are parsed in the worker and its ocean is not, so counting land-coloured pixels reads
// worker output directly. It is the only assertion here that caught every defect found.
// Chrome decodes its own screenshot for us, which keeps this dependency-free.

import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { execFileSync } from 'node:child_process';
import puppeteer from 'puppeteer-core';

const args = new Map();
for (let i = 2; i < process.argv.length; i += 2) args.set(process.argv[i], process.argv[i + 1]);
const distDir = path.resolve(args.get('--dist') ?? '');
const arm = args.get('--arm') ?? 'unknown';
const timeoutMs = Number(args.get('--timeout') ?? 30_000);
// pass   — every assertion must hold (the ordinary run)
// fail   — a NEGATIVE CONTROL: the run must fail, and --require names which assertion
//          had to be the one that failed. Without --require a control would "hold" for
//          any reason at all, including a broken rig, which is how a red control comes
//          to read as success.
// notice — the unwired-host control: assert the widget's REFUSAL, positively.
const expect = args.get('--expect') ?? 'pass';
const require_ = args.get('--require') ?? null;

// ---- what the widgets paint -------------------------------------------------

// The bundled basemap's colours (map-geometry.ts). LAND is drawn from the GeoJSON
// source — worker output. OCEAN is a flat `background` paint, and is not.
const LAND = [0x1e, 0x29, 0x3b];
const OCEAN = [0x0b, 0x12, 0x20];
// The colour this server's tiles are, so a working raster map is a flat field of it.
const TILE = [0x2f, 0x6f, 0xdf];
// WebGL output is composited before it reaches a screenshot, so colours are matched
// within a radius rather than exactly. Measured drift on this rig: ZERO — all three
// colours came back byte-exact. The tolerance is headroom for a different GPU stack,
// and is far below the ~40 that separates these three from each other.
const COLOUR_TOLERANCE = 12;

// ---- findings ---------------------------------------------------------------

const failures = [];
function check(ok, label, detail) {
  if (ok) console.log(`  ok    ${label}`);
  else {
    console.log(`  FAIL  ${label}${detail ? ` — ${detail}` : ''}`);
    failures.push(label);
  }
}
function die(message) {
  console.error(`\n==> ${arm}: ${message}\n`);
  process.exit(2);
}

if (!existsSync(distDir)) die(`no build output at ${distDir}`);
if (expect === 'fail' && !require_) die('--expect fail needs --require <assertion substring>');

// ---- the tile a raster source gets ------------------------------------------

// A 1x1 opaque PNG in one distinctive colour, built here rather than checked in as a
// base64 blob so the colour is a named value the assertions refer to. MapLibre scales
// it across the whole tile, so a working raster map is a flat field of it.
function onePixelPng([r, g, b]) {
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type, 'latin1'), data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(body) >>> 0);
    return Buffer.concat([len, body, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(1, 0);
  ihdr.writeUInt32BE(1, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // colour type: truecolour
  const raw = Buffer.from([0, r, g, b]); // one scanline, filter type 0
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', zlib.deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}
function crc32(buf) {
  let c = ~0;
  for (const byte of buf) {
    c ^= byte;
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return ~c;
}
const TILE_PNG = onePixelPng(TILE);

// ---- the server -------------------------------------------------------------

const served = []; // every request this process answered: {url, status}
const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.png': 'image/png',
  '.json': 'application/json',
  '.map': 'application/json',
};

const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1');
  const record = (status) => served.push({ url: url.pathname, status });

  if (url.pathname.startsWith('/tiles/')) {
    record(200);
    res.writeHead(200, { 'content-type': 'image/png' }).end(TILE_PNG);
    return;
  }
  // Answered rather than 404'd so that "this server served no 404s" stays an assertion
  // about the APPLICATION, not about a browser's habits.
  if (url.pathname === '/favicon.ico') {
    record(204);
    res.writeHead(204).end();
    return;
  }

  const rel = url.pathname === '/' ? '/index.html' : url.pathname;
  // Refuse to serve outside the build output: a traversal would make the rig report on
  // files no consumer would ever receive.
  const file = path.join(distDir, path.normalize(rel));
  if (!file.startsWith(distDir) || !existsSync(file)) {
    record(404);
    res.writeHead(404).end('not found');
    return;
  }
  record(200);
  res.writeHead(200, { 'content-type': TYPES[path.extname(file)] ?? 'application/octet-stream' });
  res.end(await readFile(file));
});

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;

// ---- the browser ------------------------------------------------------------

function chromePath() {
  if (process.env.CHROME_PATH) return process.env.CHROME_PATH;
  for (const name of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']) {
    try {
      return execFileSync('which', [name], { encoding: 'utf8' }).trim();
    } catch {
      /* try the next one */
    }
  }
  die('no Chrome found on PATH. Install one, or set CHROME_PATH.');
}

const browser = await puppeteer.launch({
  executablePath: chromePath(),
  headless: true,
  // 🔴 --disable-gpu does NOT mean "no WebGL": Chrome falls back to SwiftShader, which
  // reports WebGL 2.0. Measured on this box before the rig was written — and it matters,
  // because without a WebGL context MapLibre's Map constructor throws, the widget renders
  // its tile-less plain panel, and that panel places markers of its own. Every DOM
  // assertion below would pass on a page with no map in it at all.
  args: ['--headless=new', '--disable-gpu', '--no-sandbox', '--window-size=1024,768'],
});

const page = await browser.newPage();
await page.setViewport({ width: 1024, height: 768, deviceScaleFactor: 1 });

const consoleErrors = [];
page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(m.text());
});
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));

console.log(`\n==> ${arm}: driving ${origin} (dist ${distDir}, expect ${expect})`);
await page.goto(origin, { waitUntil: 'load', timeout: timeoutMs });

// The one wait: markers are the last thing the widget does, so their arrival means the
// map was constructed and the position effect ran. In `notice` mode there are no markers
// by design, so the wait is skipped rather than spent. A timeout is a finding, not an
// infrastructure problem, so it is recorded as one rather than thrown.
let markersAppeared = true;
if (expect !== 'notice') {
  try {
    await page.waitForSelector('[data-testid="map-marker"]', { timeout: timeoutMs });
  } catch {
    markersAppeared = false;
  }
}
// Markers are placed as soon as the Map is CONSTRUCTED; the worker's geometry arrives
// later. Settle for a beat so the land layer has been painted before the screenshot —
// generously, because this rig runs on a software rasteriser.
await new Promise((resolve) => setTimeout(resolve, 3_000));

const dom = await page.evaluate(() => {
  const q = (s) => document.querySelectorAll(s).length;
  return {
    markers: q('[data-testid="map-marker"]'),
    markersOnRealMap: q('[data-testid="map-marker"].maplibregl-marker'),
    canvases: q('.maplibregl-canvas'),
    plainPanels: q('[data-testid="map-plain-panel"]'),
    notices: [...document.querySelectorAll('#board *')]
      .filter(
        (el) => el.children.length === 0 && /runtime not configured/i.test(el.textContent ?? ''),
      )
      .map((el) => (el.textContent ?? '').trim()),
    workerUrl: window.__dcMapRuntime?.workerUrl ?? null,
    boardText: (document.getElementById('board')?.textContent ?? '').trim(),
    rects: [...document.querySelectorAll('.maplibregl-canvas')].map((el) => {
      const r = el.getBoundingClientRect();
      return {
        x: Math.round(r.x),
        y: Math.round(r.y),
        w: Math.round(r.width),
        h: Math.round(r.height),
      };
    }),
  };
});

// Chrome decodes its own screenshot: the shot goes back into the page as a data URL,
// onto a 2D canvas, and getImageData gives the histogram. No image library needed.
const shot = await page.screenshot({ encoding: 'base64' });
const regions = await page.evaluate(
  async (b64, rects, colours, tolerance) => {
    const img = new Image();
    img.src = `data:image/png;base64,${b64}`;
    await img.decode();
    const canvas = document.createElement('canvas');
    canvas.width = img.width;
    canvas.height = img.height;
    const ctx = canvas.getContext('2d');
    ctx.drawImage(img, 0, 0);
    return rects.map((r) => {
      if (r.w < 1 || r.h < 1) return { total: 0, matched: {}, top: [] };
      const data = ctx.getImageData(r.x, r.y, r.w, r.h).data;
      // 🔴 The named colours are counted over EVERY pixel, not read off the histogram
      // below. The histogram is truncated for legibility, and a land fraction just under
      // the truncation would have read as exactly zero — a false FAIL today, and one
      // that would look like the defect this rig hunts.
      const matched = Object.fromEntries(Object.keys(colours).map((name) => [name, 0]));
      const hist = new Map();
      for (let i = 0; i < data.length; i += 4) {
        const [pr, pg, pb] = [data[i], data[i + 1], data[i + 2]];
        for (const [name, [cr, cg, cb]] of Object.entries(colours)) {
          if (
            Math.abs(pr - cr) <= tolerance &&
            Math.abs(pg - cg) <= tolerance &&
            Math.abs(pb - cb) <= tolerance
          ) {
            matched[name] += 1;
          }
        }
        const key = `${pr},${pg},${pb}`;
        hist.set(key, (hist.get(key) ?? 0) + 1);
      }
      return {
        total: (data.length / 4) | 0,
        matched,
        top: [...hist.entries()].sort((a, b) => b[1] - a[1]).slice(0, 6),
      };
    });
  },
  shot,
  dom.rects,
  { land: LAND, ocean: OCEAN, tile: TILE },
  COLOUR_TOLERANCE,
);

await browser.close();
server.close();

// ---- reading the pixels -----------------------------------------------------

// Share of a region's pixels within tolerance of one named colour, counted over the
// whole region in the page above.
const share = (region, name) =>
  region.total === 0 ? 0 : (region.matched[name] ?? 0) / region.total;

for (const [i, region] of regions.entries()) {
  const top = region.top.map(([k, n]) => `${k}=${((n / region.total) * 100).toFixed(1)}%`).join(' ');
  const named = ['land', 'ocean', 'tile']
    .map((n) => `${n}=${(share(region, n) * 100).toFixed(1)}%`)
    .join(' ');
  console.log(`  canvas ${i} (${dom.rects[i].w}x${dom.rects[i].h}): ${named}   [top: ${top}]`);
}

// Identified by WHAT THEY SHOW rather than by DOM order, so a reordered board cannot
// quietly swap which claim is being tested: if the two regions were ever the same
// canvas, or neither showed its colour, the assertions below fail rather than pass on
// the wrong one.
const rasterIdx = regions.findIndex((r) => share(r, 'tile') > 0.5);
const landIdx = regions.findIndex(
  (r, i) => i !== rasterIdx && share(r, 'ocean') + share(r, 'land') > 0.5,
);

// ---- the assertions ---------------------------------------------------------

const tiles = served.filter((r) => r.url.startsWith('/tiles/'));
const notFound = served.filter((r) => r.status === 404);
const appScripts = served.filter((r) => r.url.endsWith('.js') || r.url.endsWith('.mjs'));

console.log(`\n${arm}: assertions`);

// Reach control FIRST, and in both modes. Every assertion below is meaningless if the
// page never loaded the application — and an empty page satisfies "no errors", "no
// 404s" and "no fallback panel" perfectly.
check(appScripts.length > 0, 'the page loaded at least one script from the build output');
check(dom.boardText.length > 0 || dom.canvases > 0, 'the board rendered something');

if (expect === 'notice') {
  // The unwired-host control, asserted POSITIVELY: the widget must say what is missing.
  // "No map appeared" would also be true of a page that failed to load at all.
  check(dom.notices.length === 2, 'both map widgets rendered the runtime-not-configured notice',
    `saw ${dom.notices.length}: ${dom.notices.join(' | ')}`);
  check(dom.canvases === 0, 'no MapLibre canvas was built', `saw ${dom.canvases}`);
  check(dom.markers === 0, 'no markers were placed', `saw ${dom.markers}`);
} else {
  // -- the host's side of the contract
  check(
    typeof dom.workerUrl === 'string' && dom.workerUrl.length > 0,
    'the host produced a worker URL string',
    `got ${JSON.stringify(dom.workerUrl)}`,
  );
  let workerPath = null;
  if (typeof dom.workerUrl === 'string') {
    try {
      const resolved = new URL(dom.workerUrl, origin);
      if (resolved.origin === origin) workerPath = resolved.pathname;
    } catch {
      /* not a URL at all; the check below reports it */
    }
  }
  check(workerPath !== null, 'the worker URL resolves against this origin', String(dom.workerUrl));

  const workerHits = workerPath ? served.filter((r) => r.url === workerPath) : [];
  check(
    workerHits.length > 0 && workerHits.every((r) => r.status === 200),
    'the browser fetched the worker URL and this server answered 200',
    workerHits.length === 0
      ? 'never requested'
      : `statuses ${[...new Set(workerHits.map((r) => r.status))].join(',')}`,
  );

  // -- the widget's side
  check(markersAppeared, 'markers appeared before the timeout');
  check(dom.canvases === 2, 'both map widgets built a MapLibre canvas', `saw ${dom.canvases}`);
  check(dom.plainPanels === 0, 'neither widget fell back to the tile-less plain panel');
  check(dom.notices.length === 0, 'neither widget rendered a refusal notice', dom.notices.join(' | '));
  check(dom.markersOnRealMap === 8, 'eight markers are attached to the real maps',
    `saw ${dom.markersOnRealMap} of ${dom.markers} total`);

  // -- what actually got painted
  check(tiles.length > 0 && tiles.every((r) => r.status === 200),
    'the raster map requested tiles from this server', `${tiles.length} requests`);
  check(rasterIdx >= 0, "a canvas is painted with this server's tiles");
  check(landIdx >= 0, 'a second canvas is painted with the bundled basemap');
  // 🔴 THE CENTRAL ASSERTION. Land comes from the GeoJSON source, which only the worker
  // can tile; ocean is a flat background paint that renders without one. Land pixels are
  // therefore proof the worker loaded, ran and delivered. Every other assertion on this
  // list has been observed to pass with a dead worker.
  const landShare = landIdx >= 0 ? share(regions[landIdx], 'land') : 0;
  check(landShare > 0.02,
    'the bundled basemap drew LAND, so the worker parsed and returned geometry',
    `land is ${(landShare * 100).toFixed(2)}% of that canvas`);

  check(notFound.length === 0, 'this server answered no 404s',
    [...new Set(notFound.map((r) => r.url))].join(' '));
}

console.log(
  `\n${arm}: ${served.length} requests served, ${tiles.length} tiles, ` +
    `${notFound.length} not-found, ${consoleErrors.length} console errors`,
);
for (const e of consoleErrors.slice(0, 6)) console.log(`  console: ${e}`);

// ---- the verdict ------------------------------------------------------------

if (expect === 'fail') {
  // A control that merely goes red has proved nothing: it goes red for a network
  // hiccup, a stale install, or a rig this edit broke. It holds only if the assertion
  // it was aimed at is the one that failed.
  const hit = failures.filter((f) => f.includes(require_));
  if (hit.length === 0) {
    console.error(
      `\n==> ${arm}: CONTROL DID NOT HOLD. Expected an assertion matching ` +
        `"${require_}" to fail; ${failures.length === 0 ? 'everything passed' : `what failed was: ${failures.join('; ')}`}\n`,
    );
    process.exit(1);
  }
  console.log(`\n==> ${arm}: CONTROL HELD — "${hit[0]}" failed, as required\n`);
  process.exit(0);
}

if (failures.length > 0) {
  console.error(`\n==> ${arm}: FAILED (${failures.length}): ${failures.join('; ')}\n`);
  process.exit(1);
}
console.log(`\n==> ${arm}: PASSED\n`);
