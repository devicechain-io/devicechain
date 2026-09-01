// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node
//
// 🔴 The node environment is REQUIRED, not a preference. This package's default is jsdom,
// whose `TextEncoder` does not produce a real `Uint8Array`; esbuild checks that invariant at
// import time and refuses to load, so the whole suite fails to collect rather than failing an
// assertion. Nothing here touches the DOM, so node is also the honest environment for it.

// 🔴 THE GATE: the PORTABLE entry of every published package writes no bundler-specific
// module specifier, and the one Vite-only entry writes exactly the two it is allowed.
//
// These packages are published to npm and a consumer builds them with whatever bundler they
// already use. A specifier like `foo?worker&url` or a bare `foo.css` is not ESM — it is one
// bundler's dialect. Vite understands both; webpack, Rollup and esbuild do not, or do
// differently.
//
// 🔴 AND NO BUILD CAN TELL YOU. Measured, not assumed: with `maplibre-gl` external — which
// it is, and must be — esbuild and tsup both build cleanly and copy
// `import("maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url")` into the output VERBATIM,
// because a specifier matching an external prefix is never resolved at all. The artifact
// ships, every gate is green, the package works under a consumer's Vite, and renders a
// silent blank map under anything else. A green pipeline over a broken artifact is strictly
// worse than a red one, so this gate had to be built on purpose.
//
// 🔴 WHY esbuild's METAFILE AND NOT A SOURCE SCAN. The first version of this file was a
// hand-written lexer that stripped comments and matched import syntax. Review broke it three
// ways in one sitting, each a single line: a regex literal containing `//` swallowed the
// rest of the line, a quote inside a regex character class flipped string state and hid a
// following import, and a template-literal dynamic import matched no pattern at all
// (`import(`…?worker&url`)`) while Vite resolves it perfectly well. All three are the same
// root cause — a lexer that is not a parser — and patching them individually would only
// have moved the boundary.
//
// So the gate asks the bundler. esbuild parses the real module graph and its metafile
// reports every import specifier it saw, static and dynamic, external and internal. Comments
// are gone because the parser removed them; template literals, regex literals and every
// other lexical trick are moot because nothing here is reading characters. It also scopes
// the claim correctly: only what is REACHABLE FROM THE ENTRY can ship, so an unreachable
// scratch file is rightly not a finding.

import * as esbuild from 'esbuild';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const PACKAGES = join(dirname(fileURLToPath(import.meta.url)), '..', '..');

/** A specifier no bundler-agnostic consumer can be expected to resolve. */
export function isBundlerDialect(specifier: string): boolean {
  // A query suffix (`?worker&url`, `?url`, `?raw`, `?inline`) is a bundler instruction.
  if (specifier.includes('?')) return true;
  // So is importing a stylesheet: Node ESM cannot load one and webpack needs a loader.
  if (/\.(css|scss|sass|less|styl)$/.test(specifier)) return true;
  return false;
}

type Graph = { specifiers: string[]; inputs: string[] };

/**
 * Every import specifier reachable from `entry`, as the bundler itself saw them.
 *
 * `packages: 'external'` mirrors the publish configuration — anything not authored in the
 * package is left unresolved, which is exactly the condition under which a dialect
 * specifier passes through untouched and unreported.
 */
async function graphFrom(entry: string): Promise<Graph> {
  let result;
  try {
    result = await esbuild.build({
      entryPoints: [entry],
      bundle: true,
      write: false,
      metafile: true,
      format: 'esm',
      platform: 'browser',
      jsx: 'automatic',
      logLevel: 'silent',
      packages: 'external',
    });
  } catch (err) {
    // A RELATIVE dialect import (`import './x.css'`) is not passed through — esbuild tries
    // to resolve it and fails for want of a loader. That is still a detection, so surface
    // the real message rather than letting it read as harness breakage.
    throw new Error(`esbuild could not build ${entry}:\n${(err as Error).message}`);
  }

  const specifiers = new Set<string>();
  for (const input of Object.values(result.metafile.inputs)) {
    for (const imp of input.imports ?? []) specifiers.add(imp.path);
  }
  return { specifiers: [...specifiers].sort(), inputs: Object.keys(result.metafile.inputs) };
}

const PORTABLE_ENTRIES = [
  ['client', join(PACKAGES, 'client', 'src', 'index.ts')],
  ['dashboards', join(PACKAGES, 'dashboards', 'src', 'index.ts')],
  ['widgets', join(PACKAGES, 'widgets', 'src', 'index.ts')],
] as const;

const VITE_ENTRY = join(PACKAGES, 'widgets', 'src', 'vite.ts');

describe('the portable entry of every published package is bundler-agnostic', () => {
  for (const [name, entry] of PORTABLE_ENTRIES) {
    it(`@devicechain/${name} reaches no bundler-dialect specifier`, async () => {
      const { specifiers } = await graphFrom(entry);

      expect(
        specifiers.filter(isBundlerDialect),
        `a bundler-specific specifier ships in the tarball verbatim and breaks every consumer not using that bundler — with no build error anywhere`,
      ).toEqual([]);
    });
  }

  // 🔴 REACH CONTROL. Every assertion above is an ABSENCE claim, and an absence claim over
  // an empty graph agrees with everything. A wrong entry path, a build that silently
  // produced nothing, or a metafile shape change would each report "no dialect" forever.
  it('the builds actually walked the real module graphs', async () => {
    for (const [name, entry] of PORTABLE_ENTRIES) {
      const { specifiers, inputs } = await graphFrom(entry);
      expect(inputs.length, `${name}: nothing was compiled`).toBeGreaterThan(3);
      expect(specifiers.length, `${name}: no imports seen at all`).toBeGreaterThan(3);
    }

    // And specifically that the widget graph reaches the library whose worker started all
    // of this — through a dynamic import, still, so the lazy boundary is intact.
    const widgets = await graphFrom(PACKAGES + '/widgets/src/index.ts');
    expect(widgets.specifiers).toContain('maplibre-gl');
    expect(widgets.inputs.some((i) => i.endsWith('src/widgets/map.tsx'))).toBe(true);
  });
});

describe('the Vite entry is the single, bounded exception', () => {
  // 🔴 THE CARVE-OUT, ASSERTED RATHER THAN PROMISED. `@devicechain/widgets/vite` exists so a
  // Vite host writes one import instead of learning MapLibre's worker problem. It is allowed
  // the dialect — but by an exact list, so "one convenience entry" cannot drift into "the
  // package writes Vite everywhere".
  it('writes exactly the two dialect specifiers it is allowed, and no others', async () => {
    const { specifiers } = await graphFrom(VITE_ENTRY);

    expect(specifiers.filter(isBundlerDialect)).toEqual([
      'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url',
      'maplibre-gl/dist/maplibre-gl.css',
    ]);
  });

  // 🔴 AND IT MUST NOT BE REACHABLE FROM THE PORTABLE ENTRY. This is the assertion that
  // makes the carve-out safe: a webpack consumer importing '@devicechain/widgets' must never
  // be handed this module. Only an explicit '@devicechain/widgets/vite' import gets it.
  it('is not reachable from the portable entry', async () => {
    const { inputs } = await graphFrom(join(PACKAGES, 'widgets', 'src', 'index.ts'));
    expect(inputs.filter((i) => i.endsWith('src/vite.ts'))).toEqual([]);
  });
});

// 🔴 POSITIVE CONTROL. The reach control proves the builds looked; this proves the checker
// SPEAKS UP when the thing is there. Both are needed — a gate that reads every file with a
// predicate that never fires passes the first.
//
// It plants the specifiers in a synthetic entry rather than in the tree, so the control
// exercises the real pipeline without a file anyone could forget to delete.
describe('the gate reports a dialect specifier when one is present', () => {
  async function specifiersOf(contents: string): Promise<string[]> {
    const result = await esbuild.build({
      stdin: { contents, resolveDir: PACKAGES, loader: 'ts', sourcefile: 'control.ts' },
      bundle: true,
      write: false,
      metafile: true,
      format: 'esm',
      platform: 'browser',
      logLevel: 'silent',
      packages: 'external',
    });
    const out = new Set<string>();
    for (const input of Object.values(result.metafile.inputs))
      for (const imp of input.imports ?? []) out.add(imp.path);
    return [...out];
  }

  const fires = async (source: string) => (await specifiersOf(source)).some(isBundlerDialect);

  it('catches the ordinary written forms', async () => {
    expect(await fires("import w from 'maplibre-gl/dist/x.mjs?worker&url'; console.log(w);")).toBe(true);
    expect(await fires("import 'maplibre-gl/dist/maplibre-gl.css';")).toBe(true);
    expect(await fires("await import('maplibre-gl/dist/maplibre-gl.css');")).toBe(true);
    expect(await fires("import u from 'some-pkg/icon.svg?url'; console.log(u);")).toBe(true);
  });

  // 🔴 THE THREE EVASIONS THAT DEFEATED THE HAND-WRITTEN LEXER. Each is pinned here so the
  // gate can never regress to a shape that misses them.
  it('catches a template-literal dynamic import (E3 — matched no lexer pattern)', async () => {
    expect(await fires('await import(`maplibre-gl/dist/x.mjs?worker&url`);')).toBe(true);
  });

  it('catches an import following a regex literal containing slashes (E1)', async () => {
    const src = "const r = /\\/\\//g; import w from 'maplibre-gl/dist/x.mjs?worker&url'; console.log(r, w);";
    expect(await fires(src)).toBe(true);
  });

  it('catches an import following a regex literal containing a quote (E2)', async () => {
    const src =
      "const r = /['\"]/u; const s = 'https://h.example'; import w from 'maplibre-gl/dist/x.mjs?worker&url'; console.log(r, s, w);";
    expect(await fires(src)).toBe(true);
  });

  // ...and stays quiet on the ordinary imports these packages are full of, so the predicate
  // cannot be satisfied by one that simply matches everything.
  it('stays quiet on ordinary specifiers, including ones merely MENTIONED in a comment', async () => {
    expect(await fires("import * as e from 'echarts/core'; console.log(e);")).toBe(false);
    expect(await fires("import type { Map } from 'maplibre-gl'; export type M = Map;")).toBe(false);
    // The documentation case that broke the previous implementation: a worked example of the
    // dialect, in a comment, must not be reported. The parser never sees it.
    expect(
      await fires("// import x from 'maplibre-gl/dist/y.mjs?worker&url';\nimport 'react';"),
    ).toBe(false);
  });
});
