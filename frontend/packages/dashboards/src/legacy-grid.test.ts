// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The conversion of a definition stored before schemaVersion 2. The numbers are worked by hand
// on the reference grid: 12 columns of (1366 - 11*8) / 12 = 106.5 px, so a converted row pitch is
// 114.5 px, against the old 40 + 8 = 48 px.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition } from './definition';
import { convertLegacyDefinition } from './legacy-grid';

const oldBox = (col: number, colSpan: number, row: number, rowSpan: number, extra: Record<string, unknown> = {}) => ({
  base: { col, colSpan, row, rowSpan, z: 0, ...extra },
});

function legacy(widgets: unknown[], canvas: Record<string, unknown> = {}) {
  return {
    schemaVersion: 1,
    title: 'Old',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 }, ...canvas },
    widgets,
  };
}

const label = (id: string, layout: unknown) => ({ id, type: 'label', layout, options: { text: id } });

describe('converting a schemaVersion 1 definition', () => {
  it('maps columns by 12/24 and rows through px at the 1366 px reference width', () => {
    const def = parseDashboardDefinition(
      legacy([label('kpi', oldBox(0, 8, 0, 3)), label('chart', oldBox(8, 16, 3, 5)), label('wide', oldBox(0, 24, 8, 6))]),
    );
    // kpi: rows 0..3 old = 0..144 px -> 0..1.26 pitches -> rows 0..1.
    expect(def.widgets[0].layout.base).toEqual({ mode: 'grid', col: 0, row: 0, cols: 4, rows: 1 });
    // chart: 144..384 px -> 1.26..3.35 -> rows 1..3.
    expect(def.widgets[1].layout.base).toEqual({ mode: 'grid', col: 4, row: 1, cols: 8, rows: 2 });
    // wide: 384..672 px -> 3.35..5.87 -> rows 3..6.
    expect(def.widgets[2].layout.base).toEqual({ mode: 'grid', col: 0, row: 3, cols: 12, rows: 3 });
    expect(def.canvas.grid).toEqual({ columns: 12, minCellSize: 56, gutter: 8, padding: 0 });
  });

  it('keeps two boxes that met at a line meeting at one (edges round, not spans)', () => {
    // 0..5 and 5..11 of 24: rounding each span alone would give 3 + 3 = 6 columns over 5.5.
    const def = parseDashboardDefinition(legacy([label('a', oldBox(0, 5, 0, 2)), label('b', oldBox(5, 6, 0, 2))]));
    const a = def.widgets[0].layout.base;
    const b = def.widgets[1].layout.base;
    expect(a.mode === 'grid' && b.mode === 'grid' && a.col + a.cols).toBe(b.mode === 'grid' && b.col);
  });

  it('drops z and the pixel offset, and pushes what they layered DOWN until nothing overlaps', () => {
    const def = parseDashboardDefinition(
      legacy([
        label('under', oldBox(0, 8, 0, 4, { z: 0 })),
        label('over', oldBox(2, 8, 1, 4, { z: 5, offset: { x: -40, y: 12 } })),
      ]),
    );
    const under = def.widgets[0].layout.base;
    const over = def.widgets[1].layout.base;
    expect(under).toEqual({ mode: 'grid', col: 0, row: 0, cols: 4, rows: 2 });
    // It keeps its column and its span, and starts where `under` ends.
    expect(over).toEqual({ mode: 'grid', col: 1, row: 2, cols: 4, rows: 2 });
    expect(JSON.stringify(def)).not.toMatch(/"z"|"offset"|colSpan|rowSpan|rowHeight|"gap"/);
  });

  it('keeps the old gap as the gutter, per axis when the axes differed, and reads a non-default column count', () => {
    const def = parseDashboardDefinition(
      legacy([label('a', { base: { col: 12, colSpan: 24, row: 0, rowSpan: 2, z: 0 } })], {
        grid: { columns: 48, gap: { row: 4, col: 12 }, rowHeight: 40 },
      }),
    );
    expect(def.canvas.grid.gutter).toEqual({ x: 12, y: 4 });
    expect(def.widgets[0].layout.base).toMatchObject({ col: 3, cols: 6 });
  });

  it('converts every breakpoint a widget carries', () => {
    const def = parseDashboardDefinition(
      legacy([label('a', { ...oldBox(0, 24, 0, 3), md: { col: 0, colSpan: 12, row: 0, rowSpan: 3, z: 0 } })], {
        breakpoints: { base: 0, md: 1200 },
      }),
    );
    expect(def.widgets[0].layout).toEqual({
      base: { mode: 'grid', col: 0, row: 0, cols: 12, rows: 1 },
      md: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 1 },
    });
    expect(def.canvas.breakpoints).toEqual({ base: 0, md: 1200 });
  });

  it('marks the result, and the next save writes the current format with no mark', () => {
    const def = parseDashboardDefinition(legacy([label('a', oldBox(0, 8, 0, 3))]));
    expect(def.convertedFrom).toBe(1);
    expect(def.schemaVersion).toBe(2);
    const saved = JSON.parse(serializeDefinition(def));
    expect(saved.schemaVersion).toBe(2);
    expect('convertedFrom' in saved).toBe(false);
    // The saved document is current: it parses with no conversion and no mark.
    expect(parseDashboardDefinition(saved).convertedFrom).toBeUndefined();
  });

  it('reads a document with no schemaVersion by its shape', () => {
    // An old box anywhere: the old format.
    const old = parseDashboardDefinition({ widgets: [label('a', oldBox(0, 8, 0, 3))] });
    expect(old.convertedFrom).toBe(1);
    expect(old.widgets[0].layout.base).toEqual({ mode: 'grid', col: 0, row: 0, cols: 4, rows: 1 });
    // An old grid alone is enough.
    expect(parseDashboardDefinition({ canvas: { grid: { columns: 24, rowHeight: 40 } }, widgets: [] }).canvas.grid.columns).toBe(12);
    // A box the old parser filled in (no spans, no z) and the current one cannot read: the old
    // format, so it opens.
    const sparse = parseDashboardDefinition({ widgets: [label('a', { base: { col: 2, row: 1 } })] });
    expect(sparse.convertedFrom).toBe(1);
    expect(sparse.widgets[0].layout.base).toEqual({ mode: 'grid', col: 1, row: 0, cols: 1, rows: 1 });
    // Neither: the current format, left exactly as written.
    const current = parseDashboardDefinition({ widgets: [label('a', { base: { mode: 'grid', col: 2, row: 0, cols: 8, rows: 3 } })] });
    expect(current.convertedFrom).toBeUndefined();
    expect(current.widgets[0].layout.base).toEqual({ mode: 'grid', col: 2, row: 0, cols: 8, rows: 3 });
  });

  it('believes a stated schemaVersion over the shape', () => {
    // Stated 2 with an old box: not converted, so the missing spans are refused.
    expect(() => parseDashboardDefinition({ schemaVersion: 2, widgets: [label('a', oldBox(0, 8, 0, 3))] })).toThrow(/cols is undefined/);
  });

  it('does not mark an empty board: there is no layout to review', () => {
    expect(parseDashboardDefinition(legacy([])).convertedFrom).toBeUndefined();
  });

  it('converts a widget of a type this build does not know, so its stored object is written back current', () => {
    const def = parseDashboardDefinition(legacy([{ id: 'x', type: 'acme/radar', layout: oldBox(0, 8, 0, 3), options: { k: 1 } }]));
    const w = def.widgets[0];
    expect(w.type).toBe('unknown-widget');
    const saved = JSON.parse(serializeDefinition(def));
    expect(saved.widgets[0]).toEqual({
      id: 'x',
      type: 'acme/radar',
      layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 1 } },
      options: { k: 1 },
    });
  });

  it('does not modify the document it was given', () => {
    const raw = legacy([label('a', oldBox(0, 8, 0, 3))]);
    const before = JSON.stringify(raw);
    convertLegacyDefinition(raw);
    parseDashboardDefinition(raw);
    expect(JSON.stringify(raw)).toBe(before);
  });
});
