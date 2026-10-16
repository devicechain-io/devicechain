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

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as esbuild from 'esbuild';
import { beforeAll, describe, expect, it } from 'vitest';

const distDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../dist');

// What the /echarts entry defines, and so what the root graph must not.
const CHART_HELPERS = ['EChart', 'buildLineOption', 'buildGaugeOption', 'resolveChartTheme', 'useChartTheme'];
const definitionOf = (name: string) => new RegExp(`\\b(?:function|const|let|var|class)\\s+${name}\\b`);

let imports: string[];
let rootInputs: string[];
let chartImports: string[];
let chartCode: string;

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

// The built files themselves, read as text: what a host bundler sees.
const distFiles = (): string[] => [
  ...readdirSync(distDir).filter((f) => f.endsWith('.js')).map((f) => path.join(distDir, f)),
  ...readdirSync(path.join(distDir, 'chunks')).filter((f) => f.endsWith('.js')).map((f) => path.join(distDir, 'chunks', f)),
];
const isEcharts = (s: string) => s === 'echarts' || s.startsWith('echarts/');
// `from "x"` and `import "x"`; a dynamic `import("x")` has a parenthesis and does not match.
const STATIC = /(?:\bfrom|\bimport)\s*["']([^"']+)["']/g;
const bareImports = (code: string) => [...code.matchAll(STATIC)].map((m) => m[1]).filter((s) => !s.startsWith('.'));
const dynamicImports = (code: string) => [...code.matchAll(/\bimport\(\s*["']([^"']+)["']\s*\)/g)].map((m) => m[1]);
// An entry plus every built file it imports STATICALLY: what a host loads with it.
function staticGraph(entry: string): string[] {
  const seen = new Set<string>();
  const queue = [path.join(distDir, entry)];
  while (queue.length > 0) {
    const file = queue.pop()!;
    if (seen.has(file)) continue;
    seen.add(file);
    for (const m of readFileSync(file, 'utf8').matchAll(STATIC)) {
      if (m[1].startsWith('.')) queue.push(path.resolve(path.dirname(file), m[1]));
    }
  }
  return [...seen];
}
function chunkNamed(stem: string): string {
  const found = readdirSync(path.join(distDir, 'chunks')).filter((f) => f.startsWith(`${stem}-`) && f.endsWith('.js'));
  expect(found, `exactly one ${stem} chunk`).toHaveLength(1);
  return path.join(distDir, 'chunks', found[0]);
}
const engineChunk = () => chunkNamed('echart-engine');
const markersChunk = () => chunkNamed('echart-markers');

beforeAll(async () => {
  const root = await graphOf('index.js');
  imports = root.imports;
  rootInputs = root.inputs;
  const chart = await graphOf('echarts.js');
  chartImports = chart.imports;
  chartCode = chart.inputs.map((f) => readFileSync(path.resolve(distDir, '..', f), 'utf8')).join('\n');
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
    // The file names alone cannot see a helper the bundler inlined into the root or into a
    // shared chunk (the dist is bundled, so its inputs are index.js and hashed chunks, never
    // src/chart/*). So read the code: no file in the root graph defines a chart helper. The
    // chart theme matters as much as the wrapper -- it imports no echarts, so the specifier
    // checks above are blind to it.
    const pkgDir = path.resolve(distDir, '..');
    const rootCode = rootInputs.map((f) => readFileSync(path.resolve(pkgDir, f), 'utf8')).join('\n');
    for (const helper of CHART_HELPERS) {
      expect(rootCode, `the root graph defines ${helper}`).not.toMatch(definitionOf(helper));
    }
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
    // ...and for the root's "defines no chart helper": the pattern finds every one HERE.
    for (const helper of CHART_HELPERS) {
      expect(chartCode, `the /echarts graph does not define ${helper}`).toMatch(definitionOf(helper));
    }
  });

  it('is where ECharts is imported -- by bare specifier, never bundled in', () => {
    expect(chartImports).toContain('echarts/core');
    expect(chartImports).toContain('echarts/charts');
  });

  // The categorical widgets (bar, pie) are SVG, so the chart engine's registration does not grow:
  // line and gauge are the only chart types it imports, and no lazily loaded chart-type chunk
  // exists beside the threshold markers. A bar or pie type here would cost ~7 KB gzip each, and a
  // second lazily loaded ECharts module makes the app bundler split the whole engine out of the
  // entry chunk (measured: about +8 KB on the initial download), so this is a budget line.
  it('registers only the line and gauge chart types, with no bar or pie module in the build', () => {
    const chartTypes = [...readFileSync(engineChunk(), 'utf8').matchAll(/import\s*\{([^}]*)\}\s*from\s*"echarts\/charts"/g)]
      .flatMap((m) => m[1].split(','))
      .map((n) => n.trim())
      .filter(Boolean)
      .sort();
    expect(chartTypes).toEqual(['GaugeChart', 'LineChart']);
    const chunks = readdirSync(path.join(distDir, 'chunks')).filter((f) => f.endsWith('.js'));
    expect(chunks.some((f) => /echart-markers/.test(f)), 'the pattern cannot see the markers chunk').toBe(true);
    expect(chunks.filter((f) => /bar|pie/i.test(f))).toEqual([]);
    for (const f of chunks) {
      expect(readFileSync(path.join(distDir, 'chunks', f), 'utf8'), `${f} imports a bar or pie chart type`).not.toMatch(/\b(BarChart|PieChart)\b/);
    }
  });

  // 🔴 The chart engine is NOT in the entry's static graph: a host's first download that imports
  // this entry carries no ECharts, and the engine (ECharts + zrender, ~170 KB gzip) arrives as ONE
  // chunk when the first chart mounts or loadChartEngine asks. One chunk because a second module
  // importing ECharts independently makes a host bundler split the engine's shared core into a
  // chunk of its own (measured +2.4 to +4.5 KB), so every echarts import lives in the engine
  // module, and the threshold markers are reached FROM it.
  it('loads ECharts on demand, from one chunk, never from the entry static graph', () => {
    const files = staticGraph('echarts.js');
    expect(files.length, 'reach control: the static graph was read').toBeGreaterThan(0);
    for (const f of files) {
      expect(bareImports(readFileSync(f, 'utf8')).filter(isEcharts), `${path.relative(distDir, f)} imports ECharts statically`).toEqual([]);
    }
    const engine = engineChunk();
    const echartsImporters = distFiles().filter((f) => bareImports(readFileSync(f, 'utf8')).some(isEcharts));
    expect(echartsImporters.map((f) => path.basename(f)).sort()).toEqual(
      [path.basename(engine), path.basename(markersChunk())].sort(),
    );
    // The engine's chart types, components and renderer: the whole engine is in that one chunk.
    expect(bareImports(readFileSync(engine, 'utf8')).filter(isEcharts).sort()).toEqual([
      'echarts/charts',
      'echarts/components',
      'echarts/core',
      'echarts/renderers',
    ]);
    // Reached by a dynamic import from the entry's graph, and the markers only from the engine.
    expect(files.some((f) => dynamicImports(readFileSync(f, 'utf8')).some((s) => s.includes(path.basename(engine))))).toBe(true);
    const markersImporters = distFiles().filter((f) =>
      dynamicImports(readFileSync(f, 'utf8')).some((s) => s.includes(path.basename(markersChunk()))),
    );
    expect(markersImporters).toEqual([engine]);
  });

  it('imports no map library and nothing from the built-in catalog', () => {
    expect(chartImports.filter((s) => s === 'maplibre-gl' || s.startsWith('maplibre-gl/'))).toEqual([]);
    expect(chartImports.filter((s) => s.startsWith('@devicechain/widgets'))).toEqual([]);
  });
});
