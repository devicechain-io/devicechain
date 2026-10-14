// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// This test reads fixtures off disk, so it needs the Node typings (see the note in
// widgets/src/options.test.ts: the reference is program-wide, not file-scoped).
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition } from './definition';
import {
  lintLayout,
  type LayoutFinding,
  type LayoutLintOptions,
  type PixelSize,
  type SpanLimits,
} from './layout-lint';
import type { DashboardDefinition, WidgetInstance } from './types';

// ---- TEMPORARY per-type table --------------------------------------------------
//
// Moves into the widget descriptors when they gain `layout` limits, and the pixel floor goes
// when constant-size square cells make a span a readable size by construction. Until then the
// numbers live here, in today's grid: 24 columns, 40 px rows, 8 px gutters. At 960 wide a
// column is ~32 px and a span of n columns is n*32.3 + (n-1)*8; at 1920 it is ~72 px.
//
// Deliberately CONSERVATIVE: each minimum is the smallest box in which the widget's parts all
// fit with room to spare, because a false error on a good board teaches people to ignore the
// lint. The frame header (title + subject line) costs ~34-50 px of height, and the rest is
// what the widget itself needs.
//
//   minSpan  - cols x rows. Rows are the binding constraint (a row is 40 px + 8 px gutter:
//              2 rows = 88 px, 3 = 136, 4 = 184, 6 = 280).
//   minPx    - the rendered size below which the content is not legible at all.
const TABLE: Record<string, { minSpan: { cols: number; rows: number }; minPx: PixelSize }> = {
  // title + subject line (~50 px) and then a value that can reach ~28 px of type.
  'latest-card': { minSpan: { cols: 6, rows: 3 }, minPx: { width: 140, height: 112 } },
  // title and a count large enough to read at a glance.
  'alarm-count': { minSpan: { cols: 4, rows: 3 }, minPx: { width: 120, height: 112 } },
  // title, an arc big enough to carry its ticks, and a readout under it.
  gauge: { minSpan: { cols: 6, rows: 3 }, minPx: { width: 140, height: 120 } },
  // title, axes with labels and at least a few plotted points.
  'timeseries-chart': { minSpan: { cols: 8, rows: 4 }, minPx: { width: 240, height: 160 } },
  // title, a header row and about three body rows at 12-13 px.
  table: { minSpan: { cols: 6, rows: 3 }, minPx: { width: 200, height: 120 } },
  // title, header row, about three alarm rows; the columns shed when narrow.
  'alarm-table': { minSpan: { cols: 8, rows: 3 }, minPx: { width: 240, height: 120 } },
  // one line of text. A row is 40 px, so one row is already enough; 2 columns at 960 are
  // ~72 px, room for a short caption.
  label: { minSpan: { cols: 2, rows: 1 }, minPx: { width: 64, height: 32 } },
  image: { minSpan: { cols: 2, rows: 2 }, minPx: { width: 64, height: 64 } },
  // title, the button and its acknowledgement line.
  'command-button': { minSpan: { cols: 5, rows: 3 }, minPx: { width: 160, height: 100 } },
  // title and a single select control.
  'entity-selector': { minSpan: { cols: 4, rows: 2 }, minPx: { width: 140, height: 80 } },
  // a map has to show enough ground to be a map.
  map: { minSpan: { cols: 6, rows: 4 }, minPx: { width: 200, height: 160 } },
};

const WIDTHS = [960, 1920];

function options(columns: number): LayoutLintOptions {
  return {
    columns,
    spanLimitsOf: (w: WidgetInstance): SpanLimits | undefined => {
      const row = TABLE[w.type];
      return row ? { minSpan: row.minSpan } : undefined;
    },
    pixelFloor: { widths: WIDTHS, minPxOf: (w) => TABLE[w.type]?.minPx },
  };
}

// ---- The committed sim boards --------------------------------------------------

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'sim-dashboards');

// Every fixture on disk, so a board a scenario adds is linted the moment its fixture lands:
// the Go guards named sitepulse and widgetlab and never saw buildingpulse.
function boards(): string[] {
  return readdirSync(FIXTURES)
    .filter((n) => n.endsWith('.json'))
    .map((n) => n.slice(0, -'.json'.length))
    .sort();
}

function load(token: string): DashboardDefinition {
  return parseDashboardDefinition(JSON.parse(readFileSync(join(FIXTURES, `${token}.json`), 'utf8')));
}

// Known failures: `board|widget|code[|breakpoint][|width]`. A RATCHET, not a waiver. The test fails on a finding
// that is not listed (a regression) AND on a listed one that no longer occurs (the fix
// landed, so the entry must go). The sitepulse fuel and payload cards are 4x2, and the fuel
// cards are titled with their slot's label (the machine's external id) over the token. The
// sitepulse re-layout slice fixes both and deletes these lines.
const KNOWN_FAILURES: ReadonlySet<string> = new Set<string>([
  'sp-dashboard|sp-fuel-sp-dozer-01|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-dozer-01|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-dozer-01|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-dozer-01|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-dozer-02|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-dozer-02|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-dozer-02|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-dozer-02|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-dozer-03|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-dozer-03|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-dozer-03|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-dozer-03|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-dozer-04|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-dozer-04|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-dozer-04|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-dozer-04|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-dozer-05|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-dozer-05|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-dozer-05|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-dozer-05|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-dozer-06|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-dozer-06|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-dozer-06|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-dozer-06|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-loader-01|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-loader-01|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-loader-01|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-loader-01|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-loader-02|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-loader-02|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-loader-02|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-loader-02|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-loader-03|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-loader-03|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-loader-03|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-loader-03|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-loader-04|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-loader-04|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-loader-04|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-loader-04|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-loader-05|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-loader-05|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-loader-05|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-loader-05|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-loader-06|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-loader-06|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-loader-06|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-loader-06|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-hauler-01|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-hauler-01|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-hauler-01|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-hauler-01|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-hauler-02|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-hauler-02|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-hauler-02|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-hauler-02|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-hauler-03|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-hauler-03|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-hauler-03|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-hauler-03|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-hauler-04|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-hauler-04|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-hauler-04|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-hauler-04|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-hauler-05|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-hauler-05|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-hauler-05|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-hauler-05|title-duplicates-subtitle',
  'sp-dashboard|sp-fuel-sp-hauler-06|below-readable-size|960',
  'sp-dashboard|sp-fuel-sp-hauler-06|below-readable-size|1920',
  'sp-dashboard|sp-fuel-sp-hauler-06|span-out-of-limits',
  'sp-dashboard|sp-fuel-sp-hauler-06|title-duplicates-subtitle',
  'sp-dashboard|sp-payload-sp-hauler-01|below-readable-size|960',
  'sp-dashboard|sp-payload-sp-hauler-01|below-readable-size|1920',
  'sp-dashboard|sp-payload-sp-hauler-01|span-out-of-limits',
  'sp-dashboard|sp-payload-sp-hauler-02|below-readable-size|960',
  'sp-dashboard|sp-payload-sp-hauler-02|below-readable-size|1920',
  'sp-dashboard|sp-payload-sp-hauler-02|span-out-of-limits',
  'sp-dashboard|sp-payload-sp-hauler-03|below-readable-size|960',
  'sp-dashboard|sp-payload-sp-hauler-03|below-readable-size|1920',
  'sp-dashboard|sp-payload-sp-hauler-03|span-out-of-limits',
  'sp-dashboard|sp-payload-sp-hauler-04|below-readable-size|960',
  'sp-dashboard|sp-payload-sp-hauler-04|below-readable-size|1920',
  'sp-dashboard|sp-payload-sp-hauler-04|span-out-of-limits',
  'sp-dashboard|sp-payload-sp-hauler-05|below-readable-size|960',
  'sp-dashboard|sp-payload-sp-hauler-05|below-readable-size|1920',
  'sp-dashboard|sp-payload-sp-hauler-05|span-out-of-limits',
  'sp-dashboard|sp-payload-sp-hauler-06|below-readable-size|960',
  'sp-dashboard|sp-payload-sp-hauler-06|below-readable-size|1920',
  'sp-dashboard|sp-payload-sp-hauler-06|span-out-of-limits',
]);

// The breakpoint and the width are part of the key, so a finding that survives at one width
// (or at one breakpoint's box) after a partial fix cannot hide behind an entry another width
// or box still earns.
const key = (board: string, f: LayoutFinding) =>
  [board, f.widgetId, f.code, f.breakpoint, f.width].filter((p) => p !== undefined).join('|');

describe('the committed sim boards', () => {
  it('include buildingpulse and widgetlab, not only sitepulse', () => {
    expect(boards()).toEqual(['bp-dashboard', 'sp-dashboard', 'wl-gallery', 'wl-stress']);
  });

  it('are described by the temporary table for every widget type they place', () => {
    // A type missing from the table would lint as an info finding and pass: the table must be
    // complete or the pixel floor silently covers less than it appears to.
    for (const token of boards()) {
      for (const w of load(token).widgets) {
        expect(TABLE, `${token}: ${w.id} is a ${w.type}, which the table does not describe`).toHaveProperty(w.type);
      }
    }
  });

  it('are judged by a table that agrees with itself: every minimum span clears its own pixel floor', () => {
    // A minSpan that renders below its own minPx makes the smallest LEGAL box an error, so
    // the two numbers would contradict each other on every board that uses it.
    for (const [type, row] of Object.entries(TABLE)) {
      const probe = parseDashboardDefinition({
        schemaVersion: 1,
        canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
        widgets: [
          { id: 'probe', type, layout: { base: { col: 0, colSpan: row.minSpan.cols, row: 0, rowSpan: row.minSpan.rows, z: 0 } } },
        ],
      });
      expect(lintLayout(probe, options(24)), type).toEqual([]);
    }
  });

  it('have exactly the known layout findings, no more and no fewer', () => {
    const seen = new Set<string>();
    const detail = new Map<string, string>();
    for (const token of boards()) {
      const def = load(token);
      for (const f of lintLayout(def, options(def.canvas.grid.columns))) {
        seen.add(key(token, f));
        detail.set(key(token, f), f.message);
      }
    }
    const fresh = [...seen].filter((k) => !KNOWN_FAILURES.has(k)).map((k) => `${k}: ${detail.get(k)}`);
    const stale = [...KNOWN_FAILURES].filter((k) => !seen.has(k));
    expect(fresh, 'new layout findings: fix the board, do not allowlist them').toEqual([]);
    expect(stale, 'allowlisted findings that no longer occur: delete these entries').toEqual([]);
  });
});

// ---- The negative control ------------------------------------------------------
//
// A gate is worth nothing until it has been seen to fail. This deliberately broken board is
// kept permanently, so removing a check from lintLayout fails here rather than quietly
// turning the fixture test above into a pass over less.

function broken(): DashboardDefinition {
  const box = (col: number, colSpan: number, row: number, rowSpan: number) => ({
    base: { col, colSpan, row, rowSpan, z: 0 },
  });
  return parseDashboardDefinition({
    schemaVersion: 1,
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0, md: 1200 } },
    slots: {
      d: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'dev-1' } },
      m7: { type: 'device', label: 'Machine 7', defaultBinding: { kind: 'device', deviceToken: 'dev-7' } },
    },
    widgets: [
      { id: 'tiny', type: 'latest-card', layout: box(0, 1, 0, 1), options: { title: 'Tiny' } },
      { id: 'overrun', type: 'gauge', layout: box(20, 8, 2, 4), options: {} },
      {
        id: 'dup',
        type: 'latest-card',
        layout: box(0, 8, 6, 3),
        options: { title: 'DEV-1' },
        datasource: { kind: 'slot', slot: 'd', measurements: ['m'] },
      },
      { id: 'fine', type: 'latest-card', layout: box(8, 8, 6, 3), options: { title: 'Fine' } },
      { id: 'mystery', type: 'not-a-widget', layout: box(0, 4, 9, 2) },
      {
        id: 'alias',
        type: 'latest-card',
        layout: box(16, 8, 6, 3),
        options: { title: 'Machine 7' },
        datasource: { kind: 'slot', slot: 'm7', measurements: ['m'] },
      },
      {
        id: 'narrow-md',
        type: 'latest-card',
        layout: { ...box(0, 8, 11, 3), md: { col: 0, colSpan: 1, row: 11, rowSpan: 3, z: 0 } },
        options: { title: 'Fine at base' },
      },
    ],
  });
}

describe('lintLayout on a deliberately broken board', () => {
  const findings = lintLayout(broken(), options(24));
  const codes = (id: string) => findings.filter((f) => f.widgetId === id).map((f) => f.code).sort();

  it('errors on a 1x1 latest-card, for its span and for its rendered size at both widths', () => {
    expect(codes('tiny')).toEqual(['below-readable-size', 'below-readable-size', 'span-out-of-limits']);
    expect(findings.filter((f) => f.widgetId === 'tiny').every((f) => f.severity === 'error')).toBe(true);
    expect(
      findings
        .filter((f) => f.code === 'below-readable-size' && f.widgetId === 'tiny')
        .map((f) => f.width)
        .sort((a, b) => (a ?? 0) - (b ?? 0)),
    ).toEqual(WIDTHS);
  });

  it('errors on a widget that overruns the columns', () => {
    const f = findings.find((x) => x.widgetId === 'overrun' && x.code === 'overrun-columns');
    expect(f?.severity).toBe('error');
    expect(f?.message).toContain('20..28 of 24');
  });

  it('warns when the title repeats the subject line, whatever the case', () => {
    expect(codes('dup')).toEqual(['title-duplicates-subtitle']);
    expect(findings.find((x) => x.widgetId === 'dup')?.severity).toBe('warning');
  });

  it('warns when the title is the bound slot label, the same entity under another name', () => {
    expect(codes('alias')).toEqual(['title-duplicates-subtitle']);
  });

  it('measures the box the active breakpoint shows at each width', () => {
    // 8 cols at base is fine at 960; the md box (1 col, from 1200 up) is what 1920 shows.
    const narrow = findings.filter((f) => f.widgetId === 'narrow-md');
    expect(narrow.filter((f) => f.code === 'below-readable-size')).toMatchObject([
      { width: 1920, breakpoint: 'md' },
    ]);
    expect(narrow.filter((f) => f.code === 'span-out-of-limits')).toMatchObject([{ breakpoint: 'md' }]);
  });

  it('errors on a span above a declared maximum', () => {
    const capped = lintLayout(broken(), {
      columns: 24,
      spanLimitsOf: () => ({ maxSpan: { cols: 6, rows: 6 } }),
    });
    expect(capped.find((f) => f.widgetId === 'fine')).toMatchObject({
      code: 'span-out-of-limits',
      severity: 'error',
    });
    expect(capped.find((f) => f.widgetId === 'fine')?.message).toContain('above the 6x6 maximum');
  });

  it('is silent about a well-formed widget', () => {
    expect(codes('fine')).toEqual([]);
  });

  it('reports a widget with no declared limits as info, not as fine', () => {
    expect(findings.filter((f) => f.widgetId === 'mystery')).toMatchObject([
      { code: 'no-layout-declared', severity: 'info' },
    ]);
  });

  it('checks the right edge at the column count it is given', () => {
    // 20..28 fits a 28-column reading, so the same box is only an overrun at 24.
    const wide = lintLayout(broken(), options(28));
    expect(wide.some((f) => f.widgetId === 'overrun' && f.code === 'overrun-columns')).toBe(false);
  });
});
