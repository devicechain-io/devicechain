// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// What the SDK's root entry actually pulls in, read off the BUILT artifact.
//
// The SDK exists so a small widget (a DOM-only card) can depend on it without inheriting the
// built-in catalog's weight. A chart helper exported from the root would drag `echarts` into
// every consumer, and a map helper `maplibre-gl`; nothing in a source scan notices, because
// the import is legal and every test still passes. So the claim is made about the artifact,
// by asking esbuild's parser for every specifier the entry's graph imports (comments and
// string contents excluded) — the same method packages/widgets/src/dist-shape.test.ts uses.
//
// `npm test` in this package runs build-packages.mjs first, so `dist` is current source.
//
// This file reads the built output, so it needs the Node typings. A triple-slash reference
// is program-wide; it is written here, the one file that needs it, rather than as a
// tsconfig `types` entry, which would stop tsc auto-including every other @types package.
/// <reference types="node" />

import { existsSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as esbuild from 'esbuild';
import { beforeAll, describe, expect, it } from 'vitest';

const distDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../dist');

let imports: string[];
let rootInputs: string[];
let chartImports: string[];

// One entry's graph: every bare specifier its modules import, and the files that make it up.
async function graphOf(entry: string): Promise<{ imports: string[]; inputs: string[] }> {
  const result = await esbuild.build({
    entryPoints: [path.join(distDir, entry)],
    bundle: true,
    write: false,
    metafile: true,
    format: 'esm',
    platform: 'browser',
    // THIS package's graph, with node_modules kept out but its bare specifiers recorded.
    packages: 'external',
    logLevel: 'silent',
  });
  return {
    imports: Object.values(result.metafile.inputs).flatMap((input) => input.imports.map((i) => i.path)),
    inputs: Object.keys(result.metafile.inputs),
  };
}

beforeAll(async () => {
  const root = await graphOf('index.js');
  imports = root.imports;
  rootInputs = root.inputs;
  chartImports = (await graphOf('echarts.js')).imports;
});

describe('the built SDK root entry', () => {
  // The reach control: every assertion below is an absence claim, which a missing or empty
  // build would satisfy trivially.
  it('is on disk and was actually read', () => {
    expect(existsSync(path.join(distDir, 'index.js'))).toBe(true);
    expect(existsSync(path.join(distDir, 'index.d.ts'))).toBe(true);
    expect(statSync(path.join(distDir, 'index.js')).size).toBeGreaterThan(2_000);
    expect(readFileSync(path.join(distDir, 'index.js'), 'utf8')).toContain('createWidgetRegistry');
    expect(imports).toContain('react');
    expect(imports).toContain('@devicechain/dashboards');
  });

  it('imports no chart or map library', () => {
    const heavy = imports.filter((s) => /^(echarts|maplibre-gl)(\/|$)/.test(s));
    expect(heavy, 'the SDK root must stay chart-free and map-free').toEqual([]);
  });

  it('imports nothing but its two declared peers', () => {
    // A shared chunk the entry imports is this package's own file (the build splits code both
    // entries use), not a dependency: esbuild reports it by its dist-relative path.
    const bare = [
      ...new Set(imports.filter((s) => !s.startsWith('.') && !s.startsWith('/') && !s.startsWith('dist/'))),
    ].sort();
    expect(bare).toEqual(['@devicechain/dashboards', 'react', 'react/jsx-runtime']);
  });

  it('does not reach the built-in widget package', () => {
    expect(imports.filter((s) => s.startsWith('@devicechain/widgets'))).toEqual([]);
  });

  // The DOM-only path, asserted on the file set rather than only on specifiers: a widget that
  // imports the SDK root for its frame, formatters, hooks and flash cue is handed none of the
  // chart wrapper, the option builders or the chart theme.
  it('does not include the chart helpers in its module graph', () => {
    const chartFiles = rootInputs.filter((f) => /(^|[/\\])(echarts\.js|chart[/\\])/.test(f));
    expect(chartFiles, 'the root entry reaches the /echarts entry').toEqual([]);
    // Reach control: the graph is the real one -- it does hold the shared building blocks.
    expect(readFileSync(path.join(distDir, 'index.js'), 'utf8')).toContain('WidgetFrame');
    expect(rootInputs.length).toBeGreaterThan(1);
  });
});

describe('the built SDK /echarts entry', () => {
  it('is on disk, with its declarations, and was actually read', () => {
    expect(existsSync(path.join(distDir, 'echarts.js'))).toBe(true);
    expect(existsSync(path.join(distDir, 'echarts.d.ts'))).toBe(true);
    expect(readFileSync(path.join(distDir, 'echarts.js'), 'utf8')).toContain('buildLineOption');
    // Reach control for the absence claims above and below.
    expect(chartImports.length).toBeGreaterThan(0);
  });

  it('is where ECharts is imported -- by bare specifier, never bundled in', () => {
    expect(chartImports).toContain('echarts/core');
    expect(chartImports).toContain('echarts/charts');
  });

  it('imports no map library and nothing from the built-in catalog', () => {
    expect(chartImports.filter((s) => s === 'maplibre-gl' || s.startsWith('maplibre-gl/'))).toEqual([]);
    expect(chartImports.filter((s) => s.startsWith('@devicechain/widgets'))).toEqual([]);
  });
});
