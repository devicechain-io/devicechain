// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The bundle-size gate: measure the built replay app (gzip) and fail the build when it grows
// past the budget in size-budget.json.
//
// Two numbers are gated, both gzip, both measured here and not estimated:
//   initial  the entry script plus everything it imports STATICALLY (and any stylesheet the
//            page links) -- what a visitor downloads before the map is ever needed. The map
//            library, its worker and the land data load only when a map widget mounts.
//   total    every file in dist/, plus the recording the page plays.
// `maxInitial` and `maxTotal` started as the measured sizes plus 10 %, so ordinary changes pass and
// an accidental dependency does not. Once the headroom is spent, a raise is DELIBERATE: the
// measured need (of every change landing together, when several are in flight) plus a small stated
// margin, with the measurement and the reason in the commit that raises it -- never `--measure`
// pasted in to make a red build green. The margin is 0.5 % (measured x 1.005, rounded up). A
// change that SHRINKS the initial load lowers `maxInitial` by the same rule in the same commit:
// headroom left in the budget is spent silently by the next change, and only a budget that
// tightens with the saving keeps it. `hardInitial` and `hardTotal` are the product targets
// (400 KB before the map loads, 1.2 MB all-in); the budget can never be set above them.
//
// The build also checks WHERE the bytes are, not only how many: see "The lazy boundaries" below.
//
//   node scripts/check-bundle-size.mjs            check dist/ against the budget
//   node scripts/check-bundle-size.mjs --measure  print the measurements and a budget
//                                                 (measured + 10 %) to paste into the file

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

export function initialFiles(dist) {
  const entry = path.join(dist, 'main.js');
  const seen = new Set();
  const queue = [entry];
  while (queue.length > 0) {
    const file = queue.pop();
    if (seen.has(file)) continue;
    seen.add(file);
    const text = readFileSync(file, 'utf8');
    for (const m of text.matchAll(STATIC_IMPORT)) queue.push(path.resolve(path.dirname(file), m[1]));
  }
  // Stylesheets the page links directly.
  const html = path.join(dist, 'index.html');
  try {
    for (const m of readFileSync(html, 'utf8').matchAll(/<link[^>]+href="([^"]+\.css)"/g)) {
      seen.add(path.resolve(dist, m[1]));
    }
  } catch {
    /* no index.html: the entry script alone is the initial load */
  }
  return [...seen];
}

export function measure(dist) {
  const initial = initialFiles(dist).reduce((n, f) => n + gz(f), 0);
  const total = walk(dist)
    .filter((f) => !f.endsWith('index.html'))
    .reduce((n, f) => n + gz(f), 0);
  return { initial, total };
}

// check returns a list of problems (empty = pass).
export function check(measured, budget) {
  const problems = [];
  const recording = budget.recordingGzip;
  if (measured.initial > budget.maxInitial) {
    problems.push(`initial load is ${measured.initial} B gzip, over the budget of ${budget.maxInitial} B`);
  }
  if (measured.total > budget.maxTotal) {
    problems.push(`total is ${measured.total} B gzip, over the budget of ${budget.maxTotal} B`);
  }
  if (budget.maxInitial > budget.hardInitial) {
    problems.push(`maxInitial ${budget.maxInitial} B is above the ${budget.hardInitial} B product target`);
  }
  if (budget.maxTotal + recording > budget.hardTotal) {
    problems.push(
      `maxTotal ${budget.maxTotal} B plus the ${recording} B recording is above the ${budget.hardTotal} B product target`,
    );
  }
  if (measured.initial > budget.hardInitial) {
    problems.push(`initial load ${measured.initial} B exceeds the ${budget.hardInitial} B product target`);
  }
  if (measured.total + recording > budget.hardTotal) {
    problems.push(`total ${measured.total} B plus the recording exceeds the ${budget.hardTotal} B product target`);
  }
  return problems;
}

// ---- The lazy boundaries, read off the artifact ----------------------------
//
// What the budget above cannot say is WHERE the bytes are: code folded back into the entry and an
// equal saving elsewhere measure the same. So each boundary that keeps code out of the first
// download is checked by name here, on every build, against the files the visitor actually gets:
// a marker string from the moved code must appear in NO initial file, and (the reach control, so
// the absence claim is not over a string the minifier rewrote) it must appear in a lazily loaded
// chunk of the expected name.
//
// Markers are string literals, because minification renames everything else. The catalogs'
// markers are read from the catalogs themselves, so a retranslation cannot break the check.
const catalogBadge = (locale) =>
  JSON.parse(readFileSync(path.resolve(here, `../src/i18n/locales/${locale}/chrome.json`), 'utf8')).replayBadge;

const embedLabel = (locale) =>
  JSON.parse(readFileSync(path.resolve(here, `../src/i18n/locales/${locale}/embed.json`), 'utf8')).embedLabelRetimed;

export function lazyBoundaries() {
  return [
    // The Spanish and Chinese chrome catalogs: fetched by setReplayLocale, English stays eager.
    { what: 'the Spanish catalog', chunk: 'chrome', markers: [catalogBadge('es')] },
    { what: 'the Chinese catalog', chunk: 'chrome', markers: [catalogBadge('zh-CN')] },
    // Widget components the sitepulse board may not need on its first frame.
    { what: 'the alarm table and alarm count', chunk: 'alarm-widgets', markers: ['Action failed', 'worst severity'] },
    { what: 'the entity selector', chunk: 'entity-selector', markers: ['No target slot configured.'] },
    { what: 'the measurement table', chunk: 'table', markers: ['No data yet'] },
    { what: 'the command button', chunk: 'control-widgets', markers: ['Couldn’t load command history.'] },
    // The chart engine: ECharts (its instance attribute) and zrender (its DOM id attribute), in ONE
    // chunk, fetched by preloadWidgets for a board holding a chart, or by the first chart to mount.
    { what: 'the chart engine (ECharts + zrender)', chunk: 'echart-engine', markers: ['_echarts_instance_', 'data-zr-dom-id'] },
    // The embed: its option parser, its player and its own words, fetched only for an embed.
    {
      what: 'the embed',
      chunk: 'EmbedPlayer',
      markers: ['board-replay: ignoring canvas=', ...['en', 'es', 'zh-CN'].map(embedLabel)],
    },
    // The light theme's tokens, fetched only for a light page.
    { what: 'the light theme', chunk: 'theme-light', markers: ['240 4.8% 95.9%'] },
    // The recording's positions and aggregates, fetched by the first map or stat that asks.
    { what: 'the recorded positions and aggregates', chunk: 'recorded-extra', markers: ['bucketMeasurements: not a window: '] },
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

// ---- Code that must not be in the build AT ALL -----------------------------
//
// The lazy boundaries above name a chunk the code moved to. Some code has no chunk in this app
// because nothing here needs it: it must be absent from EVERY file of dist/, initial or not. The
// reach control is the same idea one step back -- each marker must still exist in the package that
// owns it (so the absence is not over a string that was reworded), and that file is named here.
//
// The built-ins' option docs: the runtime descriptors carry none; the public, documented ones are a
// pure call in @devicechain/widgets that a host importing neither drops. A doc creeping back
// into a runtime descriptor, or a host reaching for the documented form, puts the whole table in
// the first download -- ~2 KB gzip, growing with every widget -- while the size budget alone would
// call it ordinary growth.
export function absentBoundaries() {
  return [
    {
      what: 'the built-in option docs',
      reach: path.resolve(here, '../../../packages/widgets/dist/index.js'),
      markers: ['Blank hides the frame heading.', 'Low end of the gauge scale.'],
    },
  ];
}

// checkAbsent returns a list of problems (empty = pass).
export function checkAbsent(dist, boundaries) {
  const problems = [];
  const js = walk(dist).filter((f) => f.endsWith('.js'));
  const text = js.map((f) => [f, readFileSync(f, 'utf8')]);
  for (const { what, reach, markers } of boundaries) {
    const owner = readFileSync(reach, 'utf8');
    for (const marker of markers) {
      if (!owner.includes(marker)) problems.push(`${what}: ${path.basename(reach)} no longer holds ${JSON.stringify(marker)}, so its absence below proves nothing`);
      const found = text.filter(([, t]) => t.includes(marker)).map(([f]) => path.basename(f));
      if (found.length > 0) problems.push(`${what}: in the build (${found.join(', ')} holds ${JSON.stringify(marker)})`);
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
      `total ${measured.total} B (budget ${budget.maxTotal}), recording allowance ${budget.recordingGzip} B`,
  );
  if (process.argv.includes('--measure')) {
    const up = (n) => Math.ceil((n * 1.1) / 1024) * 1024;
    console.log(JSON.stringify({ ...budget, maxInitial: up(measured.initial), maxTotal: up(measured.total) }, null, 2));
    return;
  }
  const problems = [...check(measured, budget), ...checkShape(dist, lazyBoundaries()), ...checkAbsent(dist, absentBoundaries())];
  if (problems.length > 0) {
    for (const p of problems) console.error(`bundle size gate: ${p}`);
    process.exit(1);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
