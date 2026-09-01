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
// The order it builds in, and the check that the tree still matches that order, are
// scripts/packages.mjs — shared with the release publisher, which needs the same list
// and would drift from a second copy of it.
//
// Pass --force to rebuild regardless of the freshness check below.

import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { PackageError, resolvePackages } from './packages.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const frontend = path.dirname(here);
const packagesDir = path.join(frontend, 'packages');
const force = process.argv.includes('--force');

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
// The scan, and the three-way drift check that keeps it honest, live in
// scripts/packages.mjs — see the reach control there for why an empty result has to
// be a failure rather than a quiet pass.
let packages;
try {
  packages = resolvePackages(packagesDir);
} catch (err) {
  if (!(err instanceof PackageError)) throw err;
  fail(err.message);
}

let builtAny = false;
for (const { name, dir: pkgDir, pkg } of packages) {
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
for (const { dir, pkg } of packages) {
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
