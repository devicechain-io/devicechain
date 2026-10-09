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
// `maxInitial` and `maxTotal` are the measured sizes plus 10 %, so ordinary changes pass and
// an accidental dependency does not. `hardInitial` and `hardTotal` are the product targets
// (400 KB before the map loads, 1.2 MB all-in); the budget can never be set above them.
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
  const problems = check(measured, budget);
  if (problems.length > 0) {
    for (const p of problems) console.error(`bundle size gate: ${p}`);
    process.exit(1);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
