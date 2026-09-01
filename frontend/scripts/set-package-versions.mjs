// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Stamp a release version into every publishable package: its own `version`, and every
// internal `@devicechain/*` range it declares.
//
//   node scripts/set-package-versions.mjs v0.14.0
//   node scripts/set-package-versions.mjs 0.14.0-0
//
// A leading `v` is accepted and stripped, so the release workflow can hand it the tag
// verbatim — the same shape the NuGet leg uses, and for the same reason: a registry
// rejects `v0.14.0`, so passing the tag unstripped has to fail loudly somewhere.
//
// 🔴 WHY NO VERSION IS COMMITTED. Every package.json in this workspace says
// `0.0.0-dev`, matching the csproj. That is not "the current version" — it is what a
// publish that FORGOT to run this script would ship, chosen so it cannot be mistaken
// for a release. This script is the only thing that puts a real number in a manifest,
// and it does so in the release job's throwaway checkout, never in a commit.
//
// 🔴 WHY THE INTERNAL RANGES MOVE WITH IT. The internal deps are EXACT-pinned peers
// (`"@devicechain/client": "0.0.0-dev"`), which is deliberate: `dashboards/src/hub.ts`
// classifies stream errors with `instanceof GraphQLRequestError` from `client`, and
// cross-copy `instanceof` is FALSE — a duplicate `client` turns a permission refusal
// into what reads as a generic outage. Exact pins that differ cannot dedupe, so the
// pin is what makes npm surface a mismatch as ERESOLVE instead of nesting a second
// copy. A package published at 0.14.0 whose peer still says `0.0.0-dev` therefore does
// not merely look untidy: it cannot be installed at all, and an npm version is
// immutable, so it stays uninstallable at that number forever.
//
// 🔴 THE APPS ARE DELIBERATELY NOT REWRITTEN. `apps/console` and `apps/dashboard` are
// private, are never published, and pin the same `0.0.0-dev` so npm keeps resolving
// them to the workspace links. Moving them would point them at a registry version that
// may not exist yet.

import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { DEPENDENCY_FIELDS, PackageError, SCOPE, resolvePackages } from './packages.mjs';

const PLACEHOLDER = '0.0.0-dev';

// Strict semver with an optional prerelease and build segment. Deliberately the same
// shape release.yml's guard enforces on the tag, so a version this accepts is one that
// already passed that gate — and one npm will accept, since npm rejects a bare `v`.
const SEMVER = /^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/;

export function normalizeVersion(raw) {
  if (typeof raw !== 'string' || raw.trim() === '') {
    throw new PackageError('no version given. Usage: node scripts/set-package-versions.mjs <version>');
  }
  const version = raw.trim().replace(/^v/, '');
  if (!SEMVER.test(version)) {
    throw new PackageError(`'${raw}' is not a semver version (expected X.Y.Z or X.Y.Z-pre)`);
  }
  if (version === PLACEHOLDER) {
    throw new PackageError(
      `refusing to "stamp" the placeholder ${PLACEHOLDER} — that is the committed value that means ` +
        'NO version was passed, and stamping it would make a forgotten version look like a deliberate one',
    );
  }
  return version;
}

// ---------------------------------------------------------------------------
// Rewrite one manifest in place, in memory. Returns the changes it made so the
// caller can assert on them: a rewriter that silently matched nothing is the failure
// this whole file exists to prevent, and it is indistinguishable from a correct run
// unless the run says what it did.
// ---------------------------------------------------------------------------
export function stampManifest(pkg, version) {
  const changes = [];

  if (pkg.version !== version) {
    changes.push(`version ${pkg.version} -> ${version}`);
    pkg.version = version;
  }

  for (const field of DEPENDENCY_FIELDS) {
    const deps = pkg[field];
    if (!deps) continue;
    for (const name of Object.keys(deps)) {
      if (!name.startsWith(SCOPE)) continue;
      if (deps[name] === version) continue;
      changes.push(`${field}.${name} ${deps[name]} -> ${version}`);
      deps[name] = version;
    }
  }

  return changes;
}

// ---------------------------------------------------------------------------
// Assert the result, rather than trusting the rewrite that just ran. Both halves
// matter and they fail differently:
//
//   - a leftover placeholder anywhere means a field this script does not know about
//     carries an internal range (a new dependency map, a nested override block), and
//     that field is what would ship broken;
//   - a version that is not the target means the write did not land — the manifest was
//     re-read from disk below, so this is checking the FILE, not the object that was
//     just edited in memory.
// ---------------------------------------------------------------------------
export function verifyManifest(name, pkg, version) {
  const problems = [];

  if (pkg.version !== version) {
    problems.push(`${name}: version is ${pkg.version}, expected ${version}`);
  }
  for (const field of DEPENDENCY_FIELDS) {
    for (const [dep, range] of Object.entries(pkg[field] ?? {})) {
      if (dep.startsWith(SCOPE) && range !== version) {
        problems.push(`${name}: ${field}.${dep} is ${range}, expected ${version}`);
      }
    }
  }
  const stray = JSON.stringify(pkg).includes(`"${PLACEHOLDER}"`);
  if (stray) {
    problems.push(
      `${name}: the placeholder ${PLACEHOLDER} still appears somewhere in the manifest — ` +
        'some field carrying an internal range was not rewritten',
    );
  }

  return problems;
}

// ---------------------------------------------------------------------------
// Entrypoint. Guarded so the tests can import the functions above without running it.
// ---------------------------------------------------------------------------
export function defaultPackagesDir() {
  const here = path.dirname(fileURLToPath(import.meta.url));
  return path.join(path.dirname(here), 'packages');
}

// `packagesDir` is a parameter rather than a constant so the test can drive the WRITING
// path over a throwaway copy of the real manifests. Reading the exported helpers alone
// would leave the part that touches disk — and the re-read assertions that follow it —
// untested, which is the half that can silently write nothing.
export function main(argv, packagesDir = defaultPackagesDir()) {
  const version = normalizeVersion(argv[0]);
  const packages = resolvePackages(packagesDir);

  for (const { dir } of packages) {
    const manifestPath = path.join(dir, 'package.json');
    const raw = readFileSync(manifestPath, 'utf8');
    const pkg = JSON.parse(raw);
    const changes = stampManifest(pkg, version);
    // Two spaces and a trailing newline: what npm itself writes, so the diff a
    // maintainer sees in a release job's checkout is only the versions.
    writeFileSync(manifestPath, `${JSON.stringify(pkg, null, 2)}\n`);
    console.log(`  ${pkg.name}`);
    for (const change of changes) console.log(`    ${change}`);
  }

  // Re-read from disk. The point is to check what was WRITTEN.
  const problems = [];
  let internalRangesSeen = 0;
  for (const { name, dir } of resolvePackages(packagesDir)) {
    const pkg = JSON.parse(readFileSync(path.join(dir, 'package.json'), 'utf8'));
    problems.push(...verifyManifest(name, pkg, version));
    for (const field of DEPENDENCY_FIELDS) {
      internalRangesSeen += Object.keys(pkg[field] ?? {}).filter((dep) => dep.startsWith(SCOPE)).length;
    }
  }

  // 🔴 The reach control for the DEPENDENCY half. resolvePackages already refuses an
  // empty package set, so "every version is right" cannot be vacuous — but "every
  // internal range is right" can be, and silently: if the scope were ever renamed, or
  // the peers rewritten as `workspace:*`, every loop above would match nothing and
  // every assertion would pass over zero fields. The chain has three internal edges
  // today (dashboards->client, widgets->{client,dashboards}) declared in two maps
  // each, so the count is comfortably above zero and asserting >0 does not pin an
  // exact number that a legitimate edit would have to come here to update.
  if (internalRangesSeen === 0) {
    problems.push(
      `no ${SCOPE}* ranges were found in any manifest — the internal-dependency rewrite matched nothing, ` +
        'so its assertions checked nothing',
    );
  }

  if (problems.length > 0) {
    throw new PackageError(`the stamped manifests are wrong:\n${problems.map((p) => `  ${p}`).join('\n')}`);
  }

  console.log(`\nStamped ${packages.length} packages at ${version} (${internalRangesSeen} internal ranges).`);
}

if (fileURLToPath(import.meta.url) === path.resolve(process.argv[1] ?? '')) {
  try {
    main(process.argv.slice(2));
  } catch (err) {
    if (!(err instanceof PackageError)) throw err;
    console.error(`\n==> set-package-versions: ${err.message}\n`);
    process.exit(1);
  }
}
