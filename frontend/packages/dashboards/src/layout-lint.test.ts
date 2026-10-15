// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The negative control for lintLayout. The committed sim boards are linted against the real
// built-in descriptors in widgets/src/sim-board-layout.test.ts: this package cannot import
// `widgets`, so what it keeps is the deliberately broken board, with a small inline set of
// limits standing in for the descriptors.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition } from './definition';
import { lintLayout, spanLimitsOfLayout, type LayoutLintOptions, type SpanLimits } from './layout-lint';
import type { DashboardDefinition, WidgetInstance } from './types';

// The two types the broken board places, as a descriptor's `layout` would declare them.
const LAYOUTS = {
  'latest-card': { defaultSpan: { cols: 3, rows: 2 }, minSpan: { cols: 3, rows: 2 } },
  gauge: { defaultSpan: { cols: 3, rows: 3 }, minSpan: { cols: 3, rows: 2 } },
  label: { defaultSpan: { cols: 3, rows: 1 } },
} as const;

function options(columns: number): LayoutLintOptions {
  return {
    columns,
    spanLimitsOf: (w: WidgetInstance): SpanLimits | undefined => spanLimitsOfLayout(LAYOUTS[w.type as keyof typeof LAYOUTS]),
  };
}

// ---- The negative control ------------------------------------------------------
//
// A gate is worth nothing until it has been seen to fail. This deliberately broken board is
// kept permanently, so removing a check from lintLayout fails here rather than quietly
// turning the fixture test above into a pass over less.

const at = (col: number, row: number, cols: number, rows: number) => ({ mode: 'grid', col, row, cols, rows });

function broken(): DashboardDefinition {
  return parseDashboardDefinition({
    schemaVersion: 2,
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8 }, sizing: 'fill', breakpoints: { base: 0, md: 1200 } },
    slots: {
      d: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'dev-1' } },
      m7: { type: 'device', label: 'Machine 7', defaultBinding: { kind: 'device', deviceToken: 'dev-7' } },
    },
    widgets: [
      { id: 'tiny', type: 'latest-card', layout: { base: at(0, 0, 1, 1) }, options: { title: 'Tiny' } },
      { id: 'overrun', type: 'gauge', layout: { base: at(10, 1, 4, 3) }, options: {} },
      {
        id: 'dup',
        type: 'latest-card',
        layout: { base: at(0, 4, 3, 2) },
        options: { title: 'DEV-1' },
        datasource: { kind: 'slot', slot: 'd', measurements: ['m'] },
      },
      { id: 'fine', type: 'latest-card', layout: { base: at(3, 4, 4, 2) }, options: { title: 'Fine' } },
      { id: 'mystery', type: 'not-a-widget', layout: { base: at(0, 6, 2, 1) } },
      {
        id: 'alias',
        type: 'latest-card',
        layout: { base: at(7, 4, 3, 2) },
        options: { title: 'Machine 7' },
        datasource: { kind: 'slot', slot: 'm7', measurements: ['m'] },
      },
      {
        id: 'narrow-md',
        type: 'latest-card',
        layout: { base: at(0, 7, 4, 2), md: at(0, 7, 1, 2) },
        options: { title: 'Fine at base' },
      },
      // A badge inset in its host's top-right corner: inside the canvas, and not flagged.
      {
        id: 'badge-in',
        type: 'label',
        layout: { base: { mode: 'relative', host: 'fine', anchor: 'top-right', offset: { x: -0.25, y: 0.25 }, size: { cols: 1, rows: 1 } } },
        options: { text: '!' },
      },
      // A caption hung off the LEFT edge of a widget that already sits at column 0.
      {
        id: 'badge-out',
        type: 'label',
        layout: { base: { mode: 'relative', host: 'dup', anchor: 'left', self: 'right', size: { cols: 1, rows: 1 } } },
        options: { text: '<' },
      },
      // One past the right edge, at md only: base keeps it inside.
      {
        id: 'badge-md',
        type: 'label',
        layout: {
          base: { mode: 'relative', host: 'alias', anchor: 'top-right', self: 'top-right', size: { cols: 1, rows: 1 } },
          md: { mode: 'relative', host: 'alias', anchor: 'top-right', self: 'top-left', offset: { x: 3, y: 0 }, size: { cols: 1, rows: 1 } },
        },
        options: { text: '>' },
      },
    ],
  });
}

describe('lintLayout on a deliberately broken board', () => {
  const findings = lintLayout(broken(), options(12));
  const codes = (id: string) => findings.filter((f) => f.widgetId === id).map((f) => f.code).sort();

  it('errors on a 1x1 latest-card, for its span', () => {
    expect(codes('tiny')).toEqual(['span-out-of-limits']);
    expect(findings.find((f) => f.widgetId === 'tiny')).toMatchObject({ severity: 'error', message: '1x1 is below the 3x2 minimum' });
  });

  it('errors on a widget that overruns the columns', () => {
    const f = findings.find((x) => x.widgetId === 'overrun' && x.code === 'overrun-columns');
    expect(f?.severity).toBe('error');
    expect(f?.message).toContain('10..14 of 12');
  });

  it('warns when the title repeats the subject line, whatever the case', () => {
    expect(codes('dup')).toEqual(['title-duplicates-subtitle']);
    expect(findings.find((x) => x.widgetId === 'dup')?.severity).toBe('warning');
  });

  it('warns when the title is the bound slot label, the same entity under another name', () => {
    expect(codes('alias')).toEqual(['title-duplicates-subtitle']);
  });

  it('checks the placement each breakpoint declares', () => {
    expect(findings.filter((f) => f.widgetId === 'narrow-md')).toMatchObject([
      { code: 'span-out-of-limits', breakpoint: 'md', message: '1x2 is below the 3x2 minimum at breakpoint md' },
    ]);
  });

  // NEGATIVE CONTROL: relative-off-canvas.
  it('errors on a relative widget whose box falls outside the canvas, and only where it does', () => {
    expect(codes('badge-out')).toEqual(['relative-off-canvas']);
    expect(findings.find((f) => f.widgetId === 'badge-out')).toMatchObject({ severity: 'error' });
    expect(findings.find((f) => f.widgetId === 'badge-out')?.message).toMatch(/^falls outside the 12-column canvas \(left -/);
    expect(findings.filter((f) => f.widgetId === 'badge-md')).toMatchObject([{ code: 'relative-off-canvas', breakpoint: 'md' }]);
    expect(codes('badge-in')).toEqual([]);
  });

  it('errors on a span above a declared maximum', () => {
    const capped = lintLayout(broken(), { columns: 12, spanLimitsOf: () => ({ maxSpan: { cols: 3, rows: 6 } }) });
    expect(capped.find((f) => f.widgetId === 'fine')).toMatchObject({ code: 'span-out-of-limits', severity: 'error' });
    expect(capped.find((f) => f.widgetId === 'fine')?.message).toContain('above the 3x6 maximum');
  });

  it('holds an axis a resize mode pins at the default span, with no maxSpan declared', () => {
    // 'fine' is 4x2. Pinning rows at 1 makes it too tall; the free column axis stays open.
    const lintAs = (layout: Parameters<typeof spanLimitsOfLayout>[0]) =>
      lintLayout(broken(), { columns: 12, spanLimitsOf: () => spanLimitsOfLayout(layout) }).filter(
        (f) => f.widgetId === 'fine' && f.code === 'span-out-of-limits',
      );
    expect(lintAs({ defaultSpan: { cols: 3, rows: 1 }, resize: 'horizontal' })).toMatchObject([
      { message: '4x2 is above the 1-row maximum' },
    ]);
    expect(lintAs({ defaultSpan: { cols: 3, rows: 2 }, resize: 'horizontal' })).toEqual([]);
    expect(lintAs({ defaultSpan: { cols: 3, rows: 2 }, resize: 'vertical' })).toMatchObject([
      { message: '4x2 is above the 3-column maximum' },
    ]);
    expect(lintAs({ defaultSpan: { cols: 3, rows: 2 } })).toEqual([]);
  });

  it('derives limits from a layout block: bounds as declared, pinned axes at the default', () => {
    expect(spanLimitsOfLayout(undefined)).toBeUndefined();
    expect(spanLimitsOfLayout({ defaultSpan: { cols: 4, rows: 2 } })).toEqual({});
    expect(
      spanLimitsOfLayout({ defaultSpan: { cols: 4, rows: 2 }, minSpan: { cols: 2, rows: 2 }, resize: 'horizontal' }),
    ).toEqual({ minSpan: { cols: 2, rows: 2 }, maxSpan: { rows: 2 } });
    expect(spanLimitsOfLayout({ defaultSpan: { cols: 4, rows: 2 }, resize: 'none' })).toEqual({
      minSpan: { cols: 4, rows: 2 },
      maxSpan: { cols: 4, rows: 2 },
    });
  });

  it('is silent about a well-formed widget', () => {
    expect(codes('fine')).toEqual([]);
  });

  it('reports a widget with no declared limits as info, not as fine', () => {
    expect(findings.filter((f) => f.widgetId === 'mystery')).toMatchObject([{ code: 'no-layout-declared', severity: 'info' }]);
  });

  it('checks the right edge at the column count it is given', () => {
    // 10..14 fits a 14-column reading, so the same placement is only an overrun at 12.
    const wide = lintLayout(broken(), options(14));
    expect(wide.some((f) => f.widgetId === 'overrun' && f.code === 'overrun-columns')).toBe(false);
  });

  it('checks a breakpoint that declares its own column count at that count', () => {
    const def = parseDashboardDefinition({
      schemaVersion: 2,
      canvas: { breakpoints: { base: 0, phone: { minWidth: 0, columns: 4 } } },
      widgets: [{ id: 'w', type: 'label', layout: { base: at(0, 0, 6, 1), phone: at(0, 0, 6, 1) }, options: { text: 'x' } }],
    });
    expect(lintLayout(def, options(12))).toMatchObject([{ code: 'overrun-columns', breakpoint: 'phone', message: 'spans columns 0..6 of 4 at breakpoint phone' }]);
  });
});
