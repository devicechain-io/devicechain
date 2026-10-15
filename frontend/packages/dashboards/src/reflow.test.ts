// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Adaptive columns and reflow. Every expectation is a number worked out by hand from the grid's
// rule (a cell is (width - padding - (columns-1) * gutter) / columns), not read back from the
// code, so a change to either side of the arithmetic fails here by value.

import { describe, expect, it } from 'vitest';

import { DEFAULT_GRID } from './grid';
import { effectiveLayout, liveColumns, reflowGrid, type ReflowItem } from './reflow';
import type { DashboardDefinition, GridPlacement } from './types';

const at = (col: number, row: number, cols: number, rows: number): GridPlacement => ({ mode: 'grid', col, row, cols, rows });
const item = (id: string, p: GridPlacement, limits?: ReflowItem['limits']): ReflowItem => ({ id, placement: p, limits });
const grid = { ...DEFAULT_GRID }; // 12 columns, 56 px min cell, 8 px gutter, no padding

describe('liveColumns', () => {
  it('keeps the authored count while its cells are at least the minimum, and stretches them', () => {
    // 12 columns need 12*56 + 11*8 = 760 px.
    expect(liveColumns(grid, 760, 12)).toBe(12);
    expect(liveColumns(grid, 960, 12)).toBe(12);
    expect(liveColumns(grid, 1920, 12)).toBe(12);
    expect(liveColumns(grid, 3840, 12)).toBe(12);
  });

  it('drops columns as the container narrows', () => {
    expect(liveColumns(grid, 759, 12)).toBe(11);
    expect(liveColumns(grid, 600, 12)).toBe(9); // floor((600+8)/64)
    expect(liveColumns(grid, 390, 12)).toBe(6); // floor((390+8)/64)
  });

  it('never goes below the phone minimum, nor above what was authored', () => {
    expect(liveColumns(grid, 200, 12)).toBe(4);
    expect(liveColumns(grid, 100, 12)).toBe(4);
    expect(liveColumns(grid, 100, 3)).toBe(3);
    expect(liveColumns(grid, 3840, 8)).toBe(8);
  });

  it('measures the cell after the padding and scales the minimum by the density', () => {
    const padded = { ...grid, padding: 20 };
    // 390 - 40 = 350 of room: floor((350+8)/64) = 5.
    expect(liveColumns(padded, 390, 12)).toBe(5);
    // A wall: 84 px cells and 12 px gutters, so 960 px holds floor(972/96) = 10 columns.
    expect(liveColumns(grid, 960, 12, 1.5)).toBe(10);
    expect(liveColumns(grid, 960, 12, 1)).toBe(12);
  });
});

describe('reflowGrid', () => {
  it('stacks wide widgets in reading order and keeps pairs of narrow ones side by side', () => {
    const out = reflowGrid(
      [
        item('c', at(8, 0, 4, 2)),
        item('a', at(0, 0, 4, 2)),
        item('b', at(4, 0, 4, 2)),
        item('d', at(0, 2, 12, 3)),
        item('e', at(0, 5, 3, 2)),
        item('f', at(3, 5, 3, 2)),
      ],
      6,
    );
    expect(Object.fromEntries(out)).toEqual({
      a: at(0, 0, 4, 2),
      b: at(0, 2, 4, 2),
      c: at(0, 4, 4, 2),
      d: at(0, 6, 6, 3), // wider than the grid: the full width
      e: at(0, 9, 3, 2),
      f: at(3, 9, 3, 2),
    });
  });

  it('reads ties left to right, then in document order', () => {
    // Two widgets on one cell: only the parser refuses that, but the pour must still be deterministic.
    const out = reflowGrid([item('x', at(0, 0, 3, 1)), item('y', at(0, 0, 3, 1))], 6);
    expect(out.get('x')).toEqual(at(0, 0, 3, 1));
    expect(out.get('y')).toEqual(at(3, 0, 3, 1));
  });

  it('clamps each span into the widget limits, and the grid has the last word', () => {
    const out = reflowGrid(
      [
        item('small', at(0, 0, 1, 1), { minSpan: { cols: 3, rows: 2 } }),
        item('big', at(3, 0, 9, 9), { maxSpan: { cols: 3, rows: 3 } }),
        item('huge', at(0, 9, 2, 2), { minSpan: { cols: 8, rows: 2 } }),
      ],
      6,
    );
    expect(out.get('small')).toEqual(at(0, 0, 3, 2));
    expect(out.get('big')).toEqual(at(3, 0, 3, 3)); // 9x9 held to its 3x3 maximum
    expect(out.get('huge')).toEqual(at(0, 3, 6, 2)); // a minSpan of 8 yields to the 6-column grid
  });

  it('never overlaps and never moves a widget above the one before it', () => {
    const out = reflowGrid([item('a', at(0, 0, 5, 4)), item('b', at(5, 0, 5, 1)), item('c', at(0, 4, 1, 1))], 6);
    const placed = [...out.values()];
    for (let i = 0; i < placed.length; i++) {
      for (let j = i + 1; j < placed.length; j++) {
        const [p, q] = [placed[i], placed[j]];
        const overlap = p.col < q.col + q.cols && q.col < p.col + p.cols && p.row < q.row + q.rows && q.row < p.row + p.rows;
        expect(overlap).toBe(false);
      }
    }
    // b cannot sit beside a (5 + 5 > 6), so it goes under it; c fills the gap beside b, not above.
    expect(out.get('b')).toEqual(at(0, 4, 5, 1));
    expect(out.get('c')).toEqual(at(5, 4, 1, 1));
  });

  it('gives the same answer whatever order the same widgets arrive in', () => {
    const items = [item('a', at(0, 0, 4, 2)), item('b', at(4, 0, 4, 2)), item('c', at(8, 0, 4, 2))];
    expect(reflowGrid(items, 6)).toEqual(reflowGrid([...items].reverse(), 6));
  });
});

function board(tablet = true): DashboardDefinition {
  const w = (id: string, layout: Record<string, GridPlacement>) =>
    ({ id, type: 'label', layout, options: {} }) as unknown as DashboardDefinition['widgets'][number];
  return {
    schemaVersion: 2,
    title: 't',
    canvas: { grid, sizing: 'fill', breakpoints: tablet ? { base: 0, tablet: { minWidth: 500, columns: 8 } } : { base: 0 } },
    widgets: [
      w('a', { base: at(0, 0, 6, 2) }),
      w('b', { base: at(6, 0, 6, 2) }),
      w('c', { base: at(0, 2, 12, 2), tablet: at(0, 2, 8, 2) }),
    ],
  } as DashboardDefinition;
}

describe('effectiveLayout', () => {
  it('draws the authored layout when the width is not measured yet', () => {
    const l = effectiveLayout(board(), 0);
    expect(l).toMatchObject({ breakpoint: 'base', columns: 12, reflowed: false });
    expect(l.grid.get('b')).toEqual(at(6, 0, 6, 2));
  });

  it('draws the authored layout at and above the width its cells fit', () => {
    for (const width of [760, 960, 1920]) {
      const l = effectiveLayout(board(false), width);
      expect(l.reflowed, String(width)).toBe(false);
      expect(l.columns, String(width)).toBe(12);
      expect(l.grid.get('c')).toEqual(at(0, 2, 12, 2));
    }
  });

  it('pours the base layout into 6 columns at 390 px, in reading order', () => {
    // 390 selects the base breakpoint (tablet starts at 500).
    const l = effectiveLayout(board(), 390);
    expect(l).toMatchObject({ breakpoint: 'base', columns: 6, reflowed: true });
    expect(Object.fromEntries(l.grid)).toEqual({
      a: at(0, 0, 6, 2),
      b: at(0, 2, 6, 2),
      c: at(0, 4, 6, 2),
    });
  });

  it('lets an authored breakpoint win: drawn as authored while it fits, reflowed only below its own count', () => {
    // "tablet" has 8 columns, which need 8*56 + 7*8 = 504 px.
    const fits = effectiveLayout(board(), 520);
    expect(fits).toMatchObject({ breakpoint: 'tablet', columns: 8, reflowed: false });
    expect(fits.grid.get('c')).toEqual(at(0, 2, 8, 2)); // its own entry, not the base one
    expect(fits.grid.get('a')).toEqual(at(0, 0, 6, 2)); // inherited from base
    const tight = effectiveLayout(board(), 503); // 8 columns would need 504
    expect(tight).toMatchObject({ breakpoint: 'tablet', columns: 7, reflowed: true });
  });
});
