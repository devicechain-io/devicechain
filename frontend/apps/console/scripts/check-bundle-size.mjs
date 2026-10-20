// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The bundle-size gate for the console: measure the built app (gzip) and fail the build when the
// first download grows past the budget in size-budget.json. It mirrors apps/board-replay's gate.
//
// Two numbers are gated, both gzip, both measured here and not estimated:
//   initial  the entry script plus everything it imports STATICALLY (and any stylesheet the page
//            links) -- what an operator downloads before any route-level page is needed. Every page
//            but the sign-in, the layout and the landing page loads on first visit.
//   total    every .js and .css file in dist/ -- the cost of the whole app, all routes.
// `maxInitial` and `maxTotal` are the measured sizes plus ~5 %, so ordinary changes pass and an
// accidental top-level import of a heavy page or library does not. Once the headroom is spent a
// raise is DELIBERATE: the measured need plus a small stated margin, with the measurement and the
// reason in the commit that raises it -- never `--measure` pasted in to make a red build green. A
// change that SHRINKS the initial load lowers the budget in the same commit, and the gate holds
// that side too: a budget more than SLACK (10 %) above the measurement fails the build.
//
// The build also checks WHERE the bytes are, not only how many: see "The lazy boundaries" below.
//
//   node scripts/check-bundle-size.mjs            check dist/ against the budget
//   node scripts/check-bundle-size.mjs --measure  print the measurements and a budget
//                                                 (measured + 5 %) to paste into the file

import { readFileSync, readdirSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const here = path.dirname(fileURLToPath(import.meta.url));

const gz = (file) => gzipSync(readFileSync(file), { level: 9 }).length;

function walk(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

// Static imports of a built chunk: `from"./x.js"` and bare `import"./x.js"`. A dynamic
// `import("./x.js")` has a parenthesis and does not match, which is the point.
const STATIC_IMPORT = /(?:\bfrom|\bimport)\s*["'](\.{1,2}\/[^"']+\.js)["']/g;

// The entry is the module script index.html loads; its name is hashed, so it is read from the page.
export function initialFiles(dist) {
  const html = readFileSync(path.join(dist, 'index.html'), 'utf8');
  const entryHref = /<script[^>]+type="module"[^>]+src="([^"]+\.js)"/.exec(html);
  if (!entryHref) throw new Error('bundle size gate: no module script in dist/index.html');
  const seen = new Set();
  const queue = [path.resolve(dist, `.${entryHref[1]}`)];
  while (queue.length > 0) {
    const file = queue.pop();
    if (seen.has(file)) continue;
    seen.add(file);
    const text = readFileSync(file, 'utf8');
    for (const m of text.matchAll(STATIC_IMPORT)) queue.push(path.resolve(path.dirname(file), m[1]));
  }
  // Stylesheets the page links directly.
  for (const m of html.matchAll(/<link[^>]+rel="stylesheet"[^>]+href="([^"]+\.css)"/g)) {
    seen.add(path.resolve(dist, `.${m[1]}`));
  }
  return [...seen];
}

export function measure(dist) {
  const initial = initialFiles(dist).reduce((n, f) => n + gz(f), 0);
  const total = walk(dist)
    .filter((f) => f.endsWith('.js') || f.endsWith('.css'))
    .reduce((n, f) => n + gz(f), 0);
  return { initial, total };
}

// How far a budget may sit above the measurement before it stops gating anything.
export const SLACK = 0.1;

// check returns a list of problems (empty = pass).
export function check(measured, budget) {
  const problems = [];
  if (measured.initial > budget.maxInitial) {
    problems.push(`initial load is ${measured.initial} B gzip, over the budget of ${budget.maxInitial} B`);
  }
  if (measured.total > budget.maxTotal) {
    problems.push(`total is ${measured.total} B gzip, over the budget of ${budget.maxTotal} B`);
  }
  // The other side: a budget far above what the build measures gates nothing, whether it was
  // raised past need or left behind by a saving. Either way the headroom is spent silently by the
  // next change, so a budget more than SLACK above the measurement fails until it is re-measured.
  for (const [name, got, max] of [
    ['maxInitial', measured.initial, budget.maxInitial],
    ['maxTotal', measured.total, budget.maxTotal],
  ]) {
    if (max > got * (1 + SLACK)) {
      problems.push(
        `${name} ${max} B is more than ${SLACK * 100} % above the measured ${got} B: lower it (--measure prints one)`,
      );
    }
  }
  return problems;
}

// ---- The lazy boundaries, read off the artifact ----------------------------
//
// What the budget above cannot say is WHERE the bytes are: code folded back into the entry and an
// equal saving elsewhere measure the same. So each boundary that keeps code out of the first
// download is checked by name here, on every build: a marker string from the moved code must appear
// in NO initial file, and (the reach control, so the absence claim is not over a string the minifier
// rewrote) it must appear in a lazily loaded chunk whose filename starts with `chunk`. Markers are
// string literals, because minification renames everything else.
export function lazyBoundaries() {
  return [
    // The chart engine: ECharts and zrender, fetched by the first chart to mount.
    { what: 'the chart engine (ECharts + zrender)', chunk: 'echart-engine', markers: ['_echarts_instance_', 'data-zr-dom-id'] },
    // The registry families (device types, assets, areas...) with their forms, and the automation
    // canvas (React Flow) the device-profile page carries.
    { what: 'the registry route and its canvas', chunk: 'RegistryRoute', markers: ['Only child nodes can use a parent extent.', 'Floating-point measurement'] },
  ];
}

// checkShape returns a list of problems (empty = pass).
export function checkShape(dist, boundaries) {
  const problems = [];
  const initial = new Set(initialFiles(dist));
  const js = walk(dist).filter((f) => f.endsWith('.js'));
  const text = new Map(js.map((f) => [f, readFileSync(f, 'utf8')]));
  for (const { what, chunk, markers } of boundaries) {
    for (const marker of markers) {
      const eager = [...initial].filter((f) => text.has(f) && text.get(f).includes(marker));
      if (eager.length > 0) {
        problems.push(`${what}: in the first download (${eager.map((f) => path.basename(f)).join(', ')} holds ${JSON.stringify(marker)})`);
      }
      const lazy = js.filter((f) => !initial.has(f) && path.basename(f).startsWith(`${chunk}-`) && text.get(f).includes(marker));
      if (lazy.length === 0) {
        problems.push(`${what}: no lazily loaded ${chunk}-*.js chunk holds ${JSON.stringify(marker)}, so its absence above proves nothing`);
      }
    }
  }
  return problems;
}

function main() {
  const dist = path.resolve(here, '../dist');
  const budgetFile = path.resolve(here, '../size-budget.json');
  const measured = measure(dist);
  const budget = JSON.parse(readFileSync(budgetFile, 'utf8'));
  console.log(
    `bundle size (gzip): initial ${measured.initial} B (budget ${budget.maxInitial}), ` +
      `total ${measured.total} B (budget ${budget.maxTotal})`,
  );
  if (process.argv.includes('--measure')) {
    const up = (n) => Math.ceil((n * 1.05) / 1024) * 1024;
    console.log(JSON.stringify({ maxInitial: up(measured.initial), maxTotal: up(measured.total) }, null, 2));
    return;
  }
  const problems = [...check(measured, budget), ...checkShape(dist, lazyBoundaries())];
  if (problems.length > 0) {
    for (const p of problems) console.error(`bundle size gate: ${p}`);
    process.exit(1);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
