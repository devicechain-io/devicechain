// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The publishable packages, in dependency order, and the check that the tree still
// matches that list.
//
// 🔴 THE ORDER IS EXPLICIT, and that is the whole point of this file. Alphabetical
// order happens to equal dependency order today — brand, client, dashboards, widgets
// — which is exactly why `npm run build --workspaces` looks like it works. It works
// by coincidence, and the coincidence breaks silently the first time a package is
// added or renamed.
//
// It breaks in two different places, which is why the list lives here rather than in
// either caller:
//   - BUILDING out of order makes a package read its dependency's `dist` from the
//     previous run, or find none at all;
//   - PUBLISHING out of order puts a package on the registry whose exact-pinned
//     internal peer does not exist yet. That one is not recoverable — an npm version
//     is immutable, so a `widgets` published before `dashboards` stays broken at that
//     version forever.
// Two copies of this list would drift, and the second failure mode is permanent.

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';

// `brand` first because it emits stylesheets the apps import; then the SDK, the
// runtime that uses it, and the widgets that use both.
export const ORDER = ['brand', 'client', 'dashboards', 'widgets'];

// The scope every internal dependency range is written in. Rewriting at release time
// keys off this prefix, so it is named once rather than spelled into each caller.
export const SCOPE = '@devicechain/';

export class PackageError extends Error {}

// ---------------------------------------------------------------------------
// resolvePackages(packagesDir) -> [{ name, dir, pkg }], in ORDER.
//
// Checked in three separate directions, because each is a different way a caller
// goes quietly wrong:
//
//  - Nothing found at all: the scan is not reaching packages/, and every check that
//    walks the result — the `files` allowlist check, the post-rewrite assertions, the
//    publish loop — is then vacuous rather than passing. This is the reach control:
//    without it, a rotted path makes every caller report success over an empty set.
//  - Named but absent: a package was renamed or removed and ORDER still names it.
//  - Present but unordered: a package was ADDED without being given a position. It
//    would simply never be built and never be published, and the consumer importing
//    it would fail at resolution with nothing pointing back here.
// ---------------------------------------------------------------------------
export function resolvePackages(packagesDir) {
  let entries;
  try {
    entries = readdirSync(packagesDir, { withFileTypes: true });
  } catch (err) {
    throw new PackageError(`cannot read ${packagesDir} — the scan is not reaching the packages (${err.code})`);
  }

  const dirs = entries
    .filter((entry) => entry.isDirectory())
    .map((entry) => path.join(packagesDir, entry.name))
    .filter((dir) => existsSync(path.join(dir, 'package.json')));

  if (dirs.length === 0) {
    throw new PackageError(`no packages found under ${packagesDir} — the scan is not reaching them`);
  }

  const found = new Set(dirs.map((dir) => path.basename(dir)));
  const missing = ORDER.filter((name) => !found.has(name));
  const unordered = [...found].filter((name) => !ORDER.includes(name));

  if (missing.length > 0) {
    throw new PackageError(`the package order names packages that are not there: ${missing.join(', ')}`);
  }
  if (unordered.length > 0) {
    throw new PackageError(
      `these packages are not in the package order, so they are never built and never published: ${unordered.join(', ')}.\n` +
        'Add each one to ORDER in scripts/packages.mjs at the position its dependencies require.',
    );
  }

  return ORDER.map((name) => {
    const dir = path.join(packagesDir, name);
    return { name, dir, pkg: JSON.parse(readFileSync(path.join(dir, 'package.json'), 'utf8')) };
  });
}

// The dependency maps a published manifest can carry an internal range in. All four
// are rewritten: `devDependencies` is in the published `package.json` too (npm does
// not strip it), and a manifest that says `0.0.0-dev` next to a real version reads as
// a half-finished release even where nothing installs it.
export const DEPENDENCY_FIELDS = ['dependencies', 'peerDependencies', 'devDependencies', 'optionalDependencies'];
