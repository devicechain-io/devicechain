// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Build every publishable frontend package, in dependency order, and then check
// that each one's `files` allowlist names things that actually exist.
//
// `npm run build:packages` from frontend/. The apps' `pre*` scripts call it too, so
// no app script can be reached with a stale `dist` underneath it — that matters
// because the apps now consume the built packages rather than their sources, which
// means "edit widgets/src, run the console's tests" would otherwise be green over
// yesterday's artifact.
//
// 🔴 THE ORDER IS EXPLICIT, and that is the whole point of this file. Alphabetical
// order happens to equal dependency order today — brand, client, dashboards, widgets
// — which is exactly why `npm run build --workspaces` looks like it works. It works
// by coincidence, and the coincidence breaks silently the first time a package is
// added or renamed: a package built before its dependency reads that dependency's
// `dist` from the PREVIOUS run, or finds none at all.
//
// Pass --force to rebuild regardless of the freshness check below.

import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const frontend = path.dirname(here);
const packagesDir = path.join(frontend, 'packages');
const force = process.argv.includes('--force');

// Dependency order, written out. `brand` first because it emits stylesheets the apps
// import; then the SDK, the runtime that uses it, and the widgets that use both.
const ORDER = ['brand', 'client', 'dashboards', 'widgets'];

function fail(message) {
  console.error(`\n==> build:packages: ${message}\n`);
  process.exit(1);
}

function run(command, args, cwd) {
  const result = spawnSync(command, args, { stdio: 'inherit', cwd });
  if (result.status !== 0) {
    fail(`\`${command} ${args.join(' ')}\` failed in ${path.relative(frontend, cwd)} (exit ${result.status})`);
  }
}

// ---------------------------------------------------------------------------
// Freshness. The apps call this on every typecheck, test, build and dev run, so it
// has to be cheap when nothing changed — but a cache that guesses wrong reintroduces
// the exact staleness this file exists to prevent. So it compares the FULL SET of
// inputs (path, size, mtime), not a newest-mtime watermark: a deleted source file
// changes the set, and a watermark cannot see a deletion at all.
// ---------------------------------------------------------------------------
function inputManifest(pkgDir) {
  const files = {};
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(full);
      } else if (entry.isFile()) {
        const stat = statSync(full);
        files[path.relative(pkgDir, full)] = `${stat.size}:${stat.mtimeMs}`;
      }
    }
  };
  for (const dir of ['src', 'tools', 'css'].map((d) => path.join(pkgDir, d))) {
    if (existsSync(dir)) walk(dir);
  }
  for (const name of ['package.json', 'tsconfig.json', 'tsconfig.build.json', 'tokens.json']) {
    const full = path.join(pkgDir, name);
    if (existsSync(full)) {
      const stat = statSync(full);
      files[name] = `${stat.size}:${stat.mtimeMs}`;
    }
  }
  // The builders themselves are inputs: change how the bundle is produced and every
  // package is stale, even though not one of their own files moved.
  for (const script of ['build-package.mjs', 'build-packages.mjs']) {
    const stat = statSync(path.join(here, script));
    files[`../../scripts/${script}`] = `${stat.size}:${stat.mtimeMs}`;
  }
  return files;
}

// 🔴 THE STAMP LIVES OUTSIDE THE PACKAGE, and that is not tidiness. It started inside
// `dist`, where deleting the artifact conveniently deleted the claim about it — and
// `npm pack --dry-run` showed it going into the tarball, because `files: ["dist"]` ships
// everything under dist. A build stamp full of local mtimes in a published package is
// noise at best, and it makes two tarballs built from identical sources differ.
//
// Moving it out costs the free invalidation, so that is replaced explicitly below: a
// build is fresh only if its OUTPUTS are still on disk as well.
const stampDir = path.join(frontend, 'node_modules', '.cache', 'dc-build');

function outputsPresent(pkgDir, pkg) {
  const targets = Object.values(pkg.exports ?? {}).flatMap((target) =>
    typeof target === 'string' ? [target] : [target?.import, target?.types],
  );
  return targets
    .filter((target) => target?.startsWith('./dist/'))
    .every((target) => existsSync(path.join(pkgDir, target)));
}

function isFresh(name, pkgDir, pkg, manifest) {
  if (!outputsPresent(pkgDir, pkg)) return false;
  const stampPath = path.join(stampDir, `${name}.json`);
  if (!existsSync(stampPath)) return false;
  try {
    return readFileSync(stampPath, 'utf8') === JSON.stringify(manifest);
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// Build.
// ---------------------------------------------------------------------------
const packageDirs = readdirSync(packagesDir, { withFileTypes: true })
  .filter((entry) => entry.isDirectory())
  .map((entry) => path.join(packagesDir, entry.name))
  .filter((dir) => existsSync(path.join(dir, 'package.json')));

// Checked BEFORE anything is built, and in three separate directions, because each is
// a different way this file goes quietly wrong.
//
//  - Nothing found at all: the scan is not reaching packages/, and every check that
//    walks this list — including the `files` allowlist check at the bottom — is then
//    vacuous rather than passing.
//  - Named but absent: a package was renamed or removed and ORDER still names it.
//  - Present but unordered: a package was ADDED without being given a position. It
//    would simply never be built, and the app importing it would fail at resolution
//    with nothing pointing back here.
const found = new Set(packageDirs.map((dir) => path.basename(dir)));
const missing = ORDER.filter((name) => !found.has(name));
const unordered = [...found].filter((name) => !ORDER.includes(name));
if (packageDirs.length === 0) {
  fail(`no packages found under ${path.relative(frontend, packagesDir)} — the scan is not reaching them`);
}
if (missing.length > 0) {
  fail(`the build order names packages that are not there: ${missing.join(', ')}`);
}
if (unordered.length > 0) {
  fail(
    `these packages are not in the build order and so are never built: ${unordered.join(', ')}.\n` +
      'Add each one to ORDER at the position its dependencies require.',
  );
}

let builtAny = false;
for (const name of ORDER) {
  const pkgDir = path.join(packagesDir, name);

  if (name === 'brand') {
    // 🔴 `brand` is the odd one out, and it is worth naming rather than smoothing
    // over: its build is a VERIFIER (`generate.mjs --check`), not a compiler. Its
    // stylesheets are committed, generated from tokens.json, and this fails if they
    // have drifted. It emits no `dist`, so it is not part of the freshness scheme
    // either — it is cheap, and it always runs.
    builtAny = true;
    run('npm', ['run', 'build', '-w', '@devicechain/brand'], frontend);
    continue;
  }

  const pkg = JSON.parse(readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
  const manifest = inputManifest(pkgDir);
  if (!force && isFresh(name, pkgDir, pkg, manifest)) continue;

  builtAny = true;
  run(process.execPath, [path.join(here, 'build-package.mjs')], pkgDir);
  mkdirSync(stampDir, { recursive: true });
  writeFileSync(path.join(stampDir, `${name}.json`), JSON.stringify(manifest));
}

// ---------------------------------------------------------------------------
// `files` is an allowlist, and npm does not check it. Measured: with `LICENSE` in
// `files` and no LICENSE on disk, `npm pack` reports one file fewer and exits 0 — no
// error, no warning. That is how a package reaches the registry with no licence text
// while its own manifest says it ships one.
// The same shape as a <PackageReadmeFile> pointing at a missing readme, and caught
// the same way: assert the file is there, rather than scanning it and reading an
// empty result as clean.
//
// This runs after the build on purpose, so `dist` is a real directory by the time
// it is checked rather than something exempted from the rule.
// ---------------------------------------------------------------------------
const problems = [];
for (const dir of packageDirs) {
  const pkg = JSON.parse(readFileSync(path.join(dir, 'package.json'), 'utf8'));
  const rel = path.relative(frontend, dir);
  if (!Array.isArray(pkg.files) || pkg.files.length === 0) {
    problems.push(`${rel}: no "files" allowlist — the tarball would ship the whole directory, tests included`);
    continue;
  }
  for (const entry of pkg.files) {
    if (!existsSync(path.join(dir, entry))) {
      problems.push(`${rel}: "files" names ${entry}, which does not exist — that entry ships nothing`);
    }
  }
}
if (problems.length > 0) {
  fail(`the published file allowlists do not match the tree:\n${problems.map((p) => `  ${p}`).join('\n')}`);
}

if (!builtAny) console.log('  packages already up to date');
