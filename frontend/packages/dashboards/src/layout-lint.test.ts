// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The negative control for lintLayout. The committed sim boards are linted against the real
// built-in descriptors in widgets/src/sim-board-layout.test.ts: this package cannot import
// `widgets`, so what it keeps is the deliberately broken board, with a small inline set of
// limits standing in for the descriptors.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition } from './definition';
import {
  lintLayout,
  spanLimitsOfLayout,
  type LayoutLintOptions,
  type PixelSize,
  type SpanLimits,
} from './layout-lint';
import type { DashboardDefinition, WidgetInstance } from './types';

// The two types the broken board places, as a descriptor's `layout` would declare them.
const LAYOUTS = {
  'latest-card': { defaultSpan: { cols: 8, rows: 3 }, minSpan: { cols: 6, rows: 3 } },
  gauge: { defaultSpan: { cols: 8, rows: 4 }, minSpan: { cols: 6, rows: 3 } },
} as const;
const MIN_PX: Record<string, PixelSize> = {
  'latest-card': { width: 140, height: 112 },
  gauge: { width: 140, height: 120 },
};

const WIDTHS = [960, 1920];

function options(columns: number): LayoutLintOptions {
  return {
    columns,
    spanLimitsOf: (w: WidgetInstance): SpanLimits | undefined =>
      spanLimitsOfLayout(LAYOUTS[w.type as keyof typeof LAYOUTS]),
    pixelFloor: { widths: WIDTHS, minPxOf: (w) => MIN_PX[w.type] },
  };
}

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
