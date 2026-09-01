// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE GATE: no published package may write a BUNDLER-SPECIFIC module specifier.
//
// These packages are published to npm, and a consumer builds them with whatever bundler
// they already use. A specifier like `foo?worker&url` or a bare `foo.css` is not ESM — it
// is one bundler's dialect. Vite understands both; webpack, Rollup and esbuild do not, or
// understand them differently.
//
// 🔴 AND THE BUILD CANNOT TELL YOU. This was measured, not assumed: with `maplibre-gl`
// listed as an external — which it is, and must be — esbuild and tsup both build cleanly
// and copy `import("maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url")` into the output
// VERBATIM, because a specifier matching an external prefix is never resolved at all. So
// the artifact ships, every gate is green, the package works under a consumer's Vite, and
// renders a silent blank map under anything else. A green pipeline over a broken artifact
// is strictly worse than a red one, and this file is one of the two things standing
// between us and it (the other is the runtime assertion in widgets/map.test.tsx).
//
// ⚠️ THIS IS FAST FEEDBACK, NOT THE AUTHORITATIVE CHECK. A source scan is a proxy: a
// computed specifier (`'…worker.mjs' + '?worker&url'`) evades any literal pattern, and so
// does a file outside the globs below. The authoritative instrument observes the ARTIFACT
// — scanning `dist/` after the build and importing the built entry under bare Node ESM —
// and arrives with the build itself. Keep both: this one names the offending line, which
// a dist scan cannot.

/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const PACKAGES = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const SCANNED = ['client', 'dashboards', 'widgets'];

function sourceFiles(dir: string): string[] {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  return entries.flatMap((entry) => {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) return sourceFiles(full);
    return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [full] : [];
  });
}

/**
 * Strip comments, respecting string and template literals.
 *
 * 🔴 THIS IS LOAD-BEARING, NOT TIDINESS. Several files in these packages EXPLAIN this very
 * rule, and map-runtime-context.tsx carries a worked example of the host wiring inside a
 * JSDoc block — a literal `import … from '…?worker&url';`. A scan over raw text would
 * report its own documentation as a violation, and the natural fix for that (loosen the
 * pattern until the docs pass) is how a gate stops being able to fire.
 *
 * 🔴 It respects quotes because a naive `//`-to-end-of-line strip mangles any line holding
 * a URL — `'https://tiles.example.com/{z}/{x}/{y}.png'` appears in this package — and a
 * mangled line is a line this scan can no longer see an import on.
 */
export function stripComments(source: string): string {
  let out = '';
  let i = 0;
  while (i < source.length) {
    const c = source[i];
    const next = source[i + 1];
    if (c === '/' && next === '/') {
      while (i < source.length && source[i] !== '\n') i++;
      continue;
    }
    if (c === '/' && next === '*') {
      i += 2;
      while (i < source.length && !(source[i] === '*' && source[i + 1] === '/')) i++;
      i += 2;
      continue;
    }
    if (c === '"' || c === "'" || c === '`') {
      const quote = c;
      out += c;
      i++;
      while (i < source.length) {
        if (source[i] === '\\') {
          out += source.slice(i, i + 2);
          i += 2;
          continue;
        }
        out += source[i];
        if (source[i] === quote) {
          i++;
          break;
        }
        i++;
      }
      continue;
    }
    out += c;
    i++;
  }
  return out;
}

/**
 * Every module specifier the file imports, static or dynamic.
 *
 * Deliberately syntax-anchored rather than a bare search for `?` or `.css`: the thing under
 * test is what gets RESOLVED, and only an import position resolves.
 */
export function importSpecifiers(source: string): string[] {
  const code = stripComments(source);
  const found: string[] = [];
  const patterns = [
    /(?:^|[\s;}])(?:import|export)\s[^;]*?from\s*['"]([^'"]+)['"]/gm, // import x from '…' / export … from '…'
    /(?:^|[\s;}])import\s*['"]([^'"]+)['"]/gm, // bare side-effect import
    /\bimport\s*\(\s*['"]([^'"]+)['"]\s*\)/gm, // dynamic import('…')
  ];
  for (const pattern of patterns) {
    for (const match of code.matchAll(pattern)) found.push(match[1]);
  }
  return found;
}

/** A specifier no bundler-agnostic consumer can be expected to resolve. */
export function isBundlerDialect(specifier: string): boolean {
  // A query suffix (`?worker&url`, `?url`, `?raw`, `?inline`) is a bundler instruction.
  if (specifier.includes('?')) return true;
  // Importing a stylesheet is a bundler instruction too — Node ESM cannot load one, and
  // webpack needs a loader configured for it.
  if (/\.(css|scss|sass|less|styl)$/.test(specifier)) return true;
  return false;
}

describe('published packages write no bundler-specific module specifiers', () => {
  const files = SCANNED.flatMap((pkg) => sourceFiles(join(PACKAGES, pkg, 'src')));

  it('has no dialect specifier in any source file', () => {
    const offenders = files.flatMap((file) =>
      importSpecifiers(readFileSync(file, 'utf8'))
        .filter(isBundlerDialect)
        .map((specifier) => `${relative(PACKAGES, file)}: ${specifier}`),
    );

    expect(
      offenders,
      'a bundler-specific specifier ships in the tarball verbatim and breaks every consumer not using that bundler — with no build error anywhere',
    ).toEqual([]);
  });

  // 🔴 THE REACH CONTROL. The assertion above is an ABSENCE claim, and an absence claim is
  // worth nothing until the thing making it is shown to have looked. A rotted path, a
  // renamed directory, or an over-eager comment stripper each produce "no offenders"
  // forever. So: prove the scan reached all three packages, reached the specific file that
  // used to hold the violation, and can still see a real import there after stripping.
  it('the scan actually reaches the sources, in every package', () => {
    expect(files.length).toBeGreaterThan(40);
    for (const pkg of SCANNED) {
      const inPackage = files.filter((f) => f.startsWith(join(PACKAGES, pkg) + '/'));
      expect(inPackage.length, `scanned nothing in packages/${pkg}`).toBeGreaterThan(3);
    }

    const mapWidget = files.find((f) => f.endsWith(join('widgets', 'src', 'widgets', 'map.tsx')));
    expect(mapWidget, 'map.tsx — the file that carried the dialect — was not scanned').toBeTruthy();

    // Comment-stripping must not have eaten the code: the real dynamic import is still
    // visible in what the scan actually examines.
    const specifiers = importSpecifiers(readFileSync(mapWidget as string, 'utf8'));
    expect(specifiers).toContain('maplibre-gl');
  });

  // 🔴 THE POSITIVE CONTROL. The reach control proves the scan opened the files; this
  // proves the pattern would have SAID something if it found the thing. Both are needed:
  // a scan that reads every file with a regex that matches nothing passes the first.
  it('the pattern fires on every dialect it is meant to catch', () => {
    const fires = (source: string) => importSpecifiers(source).some(isBundlerDialect);

    expect(fires("import w from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';")).toBe(true);
    expect(fires("import('maplibre-gl/dist/maplibre-gl.css');")).toBe(true);
    expect(fires("import 'maplibre-gl/dist/maplibre-gl.css';")).toBe(true);
    expect(fires("import icon from './icon.svg?url';")).toBe(true);
    expect(fires("import text from './notice.md?raw';")).toBe(true);
    expect(fires("import styles from './x.css?inline';")).toBe(true);
    expect(fires("export { a } from './x?worker';")).toBe(true);

    // ...and stays quiet on the ordinary imports these packages are full of, so it cannot
    // be satisfied by a pattern that simply matches everything.
    expect(fires("import * as echarts from 'echarts/core';")).toBe(false);
    expect(fires("import { Map } from 'maplibre-gl';")).toBe(false);
    expect(fires("import type { Foo } from '@devicechain/client';")).toBe(false);
    expect(fires("import('./natural-earth-data');")).toBe(false);
  });

  // 🔴 And the stripper itself, because it is the one component whose FAILURE MODE IS
  // SILENCE: strip too much and every file looks clean.
  it('comment stripping removes documentation without removing code', () => {
    const documented = [
      "// Vite spells it `?worker&url`, webpack does not.",
      '/**',
      " * import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';",
      ' */',
      "import { real } from './real-module';",
    ].join('\n');

    expect(importSpecifiers(documented)).toEqual(['./real-module']);

    // A `//` inside a string literal is not a comment. If it were treated as one, the rest
    // of that line — potentially including an import — would vanish from the scan.
    const withUrl = "const tiles = 'https://tiles.example.com/{z}/{x}/{y}.png';\nimport('./after');";
    expect(importSpecifiers(withUrl)).toEqual(['./after']);
  });
});
