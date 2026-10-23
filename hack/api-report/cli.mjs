// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command behind hack/check-npm-api.sh. Exit 0 = clean, 1 = a finding (a drifted report,
// an undeclared break, a stale declaration), 2 = it could not run (nothing was compared).
//
//   node cli.mjs write   --frontend <dir> --out <dir>
//   node cli.mjs check   --frontend <dir> --reports <dir>
//   node cli.mjs compat  --frontend <dir> --base <dir> --baseline <tag> --breaks <file>
//   node cli.mjs emit    --frontend <dir> --base <dir>   declarations for an extracted baseline tree

import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { diffModels, parseBreaks, reconcile } from './compat.mjs';
import { buildModel, renderReport, workspacePaths } from './model.mjs';

class UsageError extends Error {}

function args(argv) {
  const [cmd, ...rest] = argv;
  const opts = {};
  for (let i = 0; i < rest.length; i += 2) {
    const k = rest[i];
    if (!k.startsWith('--') || rest[i + 1] === undefined) throw new UsageError(`bad argument: ${k}`);
    opts[k.slice(2)] = rest[i + 1];
  }
  return { cmd, opts };
}

function need(opts, ...names) {
  for (const n of names) if (!opts[n]) throw new UsageError(`--${n} is required`);
}

/** The ordered package list, from the frontend's own source of truth. */
async function packageOrder(frontend) {
  const mod = await import(pathToFileURL(path.resolve(frontend, 'scripts/packages.mjs')).href);
  return mod.ORDER;
}

// `root` is a frontend tree (the working one or an extracted baseline); the package LIST is
// always the working tree's, so a package the baseline lacked shows as new, not as an error.
async function models(frontend, root = frontend) {
  const result = new Map();
  const paths = workspacePaths(path.resolve(root, 'packages'));
  for (const name of await packageOrder(frontend)) {
    const dir = path.resolve(root, 'packages', name);
    result.set(name, existsSync(path.join(dir, 'package.json')) ? { dir, model: buildModel(dir, paths) } : null);
  }
  return result;
}

const pkgName = (frontend, name) =>
  JSON.parse(readFileSync(path.resolve(frontend, 'packages', name, 'package.json'), 'utf8')).name;

async function write(opts) {
  need(opts, 'frontend', 'out');
  mkdirSync(opts.out, { recursive: true });
  for (const [name, built] of await models(opts.frontend)) {
    if (!built) throw new Error(`packages/${name} is missing`);
    if (built.model.size === 0) throw new Error(`${name}: the model is empty; a report of nothing is not a report`);
    writeFileSync(path.join(opts.out, `${name}.api.md`), renderReport(pkgName(opts.frontend, name), built.model));
  }
}

async function check(opts) {
  need(opts, 'frontend', 'reports');
  let drift = 0;
  const order = await packageOrder(opts.frontend);
  const committed = existsSync(opts.reports) ? readdirSync(opts.reports).filter((f) => f.endsWith('.api.md')) : [];
  for (const f of committed) {
    if (!order.includes(f.replace(/\.api\.md$/, ''))) {
      console.log(`FAIL ${opts.reports}/${f} reports a package that is not published; delete it`);
      drift++;
    }
  }
  for (const [name, built] of await models(opts.frontend)) {
    if (!built) throw new Error(`packages/${name} is missing`);
    const want = renderReport(pkgName(opts.frontend, name), built.model);
    const file = path.join(opts.reports, `${name}.api.md`);
    if (!existsSync(file)) {
      console.log(`FAIL ${file} is missing; run hack/check-npm-api.sh --update and commit it`);
      drift++;
      continue;
    }
    const have = readFileSync(file, 'utf8').replace(/\r\n/g, '\n');
    if (have === want) continue;
    drift++;
    const a = have.split('\n');
    const b = want.split('\n');
    const gone = a.filter((l) => !b.includes(l));
    const added = b.filter((l) => !a.includes(l));
    console.log(`FAIL ${pkgName(opts.frontend, name)}: the public API no longer matches ${path.relative(process.cwd(), file)}`);
    for (const l of gone.slice(0, 8)) console.log(`FAIL   - ${l}`);
    for (const l of added.slice(0, 8)) console.log(`FAIL   + ${l}`);
    if (gone.length + added.length > 16) console.log(`FAIL   ... and ${gone.length + added.length - 16} more lines`);
  }
  if (drift > 0) {
    console.log(`FAIL ${drift} report(s) out of step with the code. If the change to the API is intended, run hack/check-npm-api.sh --update and commit the reports: that diff is the review.`);
    return 1;
  }
  console.log(`npm-api: ${order.length} reports match the built packages`);
  return 0;
}

async function compat(opts) {
  need(opts, 'frontend', 'base', 'baseline', 'breaks');
  const head = await models(opts.frontend);
  const base = await models(opts.frontend, opts.base);
  const breaksText = existsSync(opts.breaks) ? readFileSync(opts.breaks, 'utf8') : '';
  const { entries, problems } = parseBreaks(breaksText);
  let rc = 0;
  for (const p of problems) {
    console.log(`FAIL ${opts.breaks}: ${p}`);
    rc = 1;
  }
  const changes = [];
  for (const [name, h] of head) {
    if (!h) throw new Error(`packages/${name} is missing`);
    const b = base.get(name);
    const pkg = pkgName(opts.frontend, name);
    if (!b) {
      console.log(`${pkg}: not in ${opts.baseline}; everything it exports is new`);
      continue;
    }
    for (const c of diffModels(b.model, h.model)) changes.push({ ...c, pkg });
  }
  const r = reconcile(changes, entries, opts.baseline);
  for (const f of r.failures) console.log(f);
  for (const d of r.declared) console.log(`declared ${opts.baseline} ${d.pkg} ${d.id} ${d.kind} ${d.reason}`);
  for (const e of r.expired) console.log(`expired ${e.baseline} ${e.pkg} ${e.id} ${e.kind} (the baseline is ${opts.baseline}; delete the line)`);
  for (const d of r.dangerous) console.log(`dangerous ${d.pkg} ${d.id} ${d.kind} ${d.detail}`);
  const breaking = changes.filter((c) => c.level === 'breaking').length;
  console.log(
    `npm-api: against ${opts.baseline}: ${breaking} breaking (${r.declared.length} declared), ` +
      `${r.dangerous.length} dangerous, ${r.safe.length} safe; ${r.failures.length} failed`,
  );
  if (r.failures.length > 0) rc = 1;
  return rc;
}

async function emit(opts) {
  need(opts, 'frontend', 'base');
  const { tscCommand, runSync } = await import(pathToFileURL(path.resolve(opts.frontend, 'scripts/launch.mjs')).href);
  for (const name of await packageOrder(opts.frontend)) {
    const dir = path.resolve(opts.base, 'packages', name);
    if (!existsSync(path.join(dir, 'package.json'))) continue;
    if (!existsSync(path.join(dir, 'tsconfig.build.json'))) continue; // brand: no declarations
    // The baseline's siblings resolve to the baseline's own declarations (emitted just before,
    // in dependency order), not to the working tree's through node_modules.
    const cfg = path.join(dir, 'tsconfig.api-emit.json');
    writeFileSync(cfg, JSON.stringify({ extends: './tsconfig.build.json', compilerOptions: { paths: workspacePaths(path.resolve(opts.base, 'packages')) } }));
    const r = runSync(tscCommand(['-p', 'tsconfig.api-emit.json'], { from: dir }), { cwd: dir, stdio: 'inherit' });
    if (r.status !== 0) throw new Error(`${name}: declaration emit failed (tsc exited ${r.status})`);
  }
}

try {
  const { cmd, opts } = args(process.argv.slice(2));
  const run = { write, check, compat, emit }[cmd];
  if (!run) throw new UsageError(`unknown command: ${cmd ?? '(none)'}`);
  const rc = (await run(opts)) ?? 0;
  process.exit(rc);
} catch (err) {
  console.error(`api-report: ${err.message}`);
  process.exit(2);
}
