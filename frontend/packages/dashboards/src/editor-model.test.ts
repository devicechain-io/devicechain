// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { isDirty } from './definition';
import {
  addWidget,
  baseBox,
  bringToFront,
  deleteWidget,
  gridBoxToPx,
  pxToGridBox,
  setCanvasGrid,
  setTitle,
  setWidgetBox,
  updateWidget,
  type GridGeometry,
} from './editor-model';
import type { DashboardDefinition } from './types';

const box = (over = {}) => ({ col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0, ...over });

function def(): DashboardDefinition {
  return {
    schemaVersion: 1,
    title: 'Test',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [
      { id: 'a', type: 'gauge', layout: { base: box({ z: 1 }) } },
      { id: 'b', type: 'label', layout: { base: box({ col: 10, z: 2 }) } },
    ],
  };
}

describe('setWidgetBox', () => {
  it('replaces only the target widget’s base box', () => {
    const next = setWidgetBox(def(), 'a', box({ col: 5, row: 6, colSpan: 8, rowSpan: 9, z: 1 }));
    expect(baseBox(next.widgets[0])).toEqual({ col: 5, colSpan: 8, row: 6, rowSpan: 9, z: 1 });
    expect(baseBox(next.widgets[1])).toEqual(box({ col: 10, z: 2 })); // untouched
  });
});

describe('deleteWidget', () => {
  it('removes the widget by id', () => {
    const next = deleteWidget(def(), 'a');
    expect(next.widgets.map((w) => w.id)).toEqual(['b']);
  });
});

describe('bringToFront', () => {
  it('raises a widget above the current max z', () => {
    const next = bringToFront(def(), 'a'); // a.z=1, max=2 → a.z=3
    expect(baseBox(next.widgets[0]).z).toBe(3);
  });
  it('is a no-op when already on top', () => {
    const d = def();
    expect(bringToFront(d, 'b')).toBe(d); // b already has max z
  });
  it('breaks a z-tie by bumping above the other widget', () => {
    const tied: DashboardDefinition = {
      ...def(),
      widgets: [
        { id: 'a', type: 'gauge', layout: { base: box({ z: 2 }) } },
        { id: 'b', type: 'label', layout: { base: box({ col: 10, z: 2 }) } },
      ],
    };
    const next = bringToFront(tied, 'a'); // tie at z=2 → a bumps to 3
    expect(baseBox(next.widgets[0]).z).toBe(3);
  });
  it('is a no-op for a lone widget at z=0', () => {
    const lone: DashboardDefinition = {
      ...def(),
      widgets: [{ id: 'a', type: 'gauge', layout: { base: box({ z: 0 }) } }],
    };
    expect(bringToFront(lone, 'a')).toBe(lone);
  });
});

describe('updateWidget', () => {
  it('replaces the matching widget and leaves others untouched', () => {
    const d = def();
    const next = updateWidget(d, 'a', {
      id: 'a',
      type: 'latest-card',
      layout: { base: box({ z: 1 }) },
      options: { title: 'Renamed' },
    });
    expect(next.widgets[0]).toEqual({
      id: 'a',
      type: 'latest-card',
      layout: { base: box({ z: 1 }) },
      options: { title: 'Renamed' },
    });
    expect(next.widgets[1]).toBe(d.widgets[1]); // untouched
  });
});

describe('addWidget', () => {
  it('appends a widget with a unique id, on top, and default options', () => {
    const d = def();
    const { definition, id } = addWidget(d, 'gauge');
    expect(definition.widgets).toHaveLength(3);
    const added = definition.widgets[2];
    expect(added.id).toBe(id);
    expect(added.id).not.toBe('a');
    expect(added.id).not.toBe('b');
    expect(added.type).toBe('gauge');
    expect(baseBox(added).z).toBe(3); // max existing z (2) + 1
    expect(added.datasource).toBeUndefined();
    expect(added.options).toEqual({ title: 'Gauge' });
  });

  it('humanizes a hyphenated type into the default title', () => {
    const { definition } = addWidget(def(), 'timeseries-chart');
    expect(definition.widgets[2].options).toEqual({ title: 'Timeseries chart' });
  });

  it('uses placeholder text for a label', () => {
    const { definition } = addWidget(def(), 'label');
    expect(definition.widgets[2].options).toEqual({ text: 'New label' });
  });
});

describe('setTitle', () => {
  it('updates the title', () => {
    expect(setTitle(def(), 'Renamed').title).toBe('Renamed');
  });
});

describe('isDirty', () => {
  it('is false for an unchanged copy and true after an edit', () => {
    const d = def();
    expect(isDirty(d, def())).toBe(false);
    expect(isDirty(setTitle(d, 'x'), d)).toBe(true);
  });
});

// colWidth 100 / gaps 0 → strides 100 (col) and 50 (row): round pixels to span lines.
const geom: GridGeometry = { colWidth: 100, colGap: 0, rowHeight: 50, rowGap: 0 };

describe('pxToGridBox', () => {
  it('snaps pixels to grid span lines and clamps', () => {
    // x 205 → col 2, w 290 → colSpan 3; y 120 → row 2, h 160 → rowSpan 3.
    expect(pxToGridBox({ x: 205, y: 120, w: 290, h: 160 }, geom, 2)).toEqual({
      col: 2,
      colSpan: 3,
      row: 2,
      rowSpan: 3,
      z: 2,
    });
    // negative/zero clamps to col,row>=0 and spans>=1.
    expect(pxToGridBox({ x: -20, y: -1, w: 2, h: 0 }, geom, 0)).toEqual({
      col: 0,
      colSpan: 1,
      row: 0,
      rowSpan: 1,
      z: 0,
    });
  });

  it('un-offsets before snapping and preserves the offset', () => {
    const offset = { x: 30, y: 10 };
    // The rect is the offset position; un-offsetting lands it back on col 2 / row 2.
    expect(pxToGridBox({ x: 230, y: 130, w: 100, h: 50 }, geom, 1, offset)).toEqual({
      col: 2,
      colSpan: 1,
      row: 2,
      rowSpan: 1,
      z: 1,
      offset,
    });
  });
});

describe('gridBoxToPx', () => {
  it('renders a span box to a pixel rect', () => {
    const b = { col: 2, colSpan: 3, row: 2, rowSpan: 3, z: 0 };
    expect(gridBoxToPx(b, geom)).toEqual({ x: 200, y: 100, w: 300, h: 150 });
  });

  it('adds the signed offset to the position', () => {
    const b = { col: 1, colSpan: 1, row: 1, rowSpan: 1, z: 0, offset: { x: 5, y: -3 } };
    expect(gridBoxToPx(b, geom)).toEqual({ x: 105, y: 47, w: 100, h: 50 });
  });
});

// The two functions are inverses: a span box → pixels → span box is the identity.
// Exercised WITH a nonzero gap so the `(span-1)*gap` term and the `+gap` snap
// correction are actually pinned (the zero-gap `geom` above degenerates to cell math).
describe('gridBoxToPx / pxToGridBox round-trip', () => {
  const gapped: GridGeometry = { colWidth: 33, colGap: 8, rowHeight: 40, rowGap: 4 };

  it('is the identity for representative boxes (nonzero gap)', () => {
    for (const b of [
      { col: 0, colSpan: 1, row: 0, rowSpan: 1, z: 0 },
      { col: 4, colSpan: 3, row: 2, rowSpan: 5, z: 2 },
      { col: 1, colSpan: 1, row: 1, rowSpan: 1, z: 0, offset: { x: 5, y: -3 } },
    ]) {
      expect(pxToGridBox(gridBoxToPx(b, gapped), gapped, b.z, b.offset)).toEqual(b);
    }
  });

  it('renders a gapped multi-span width as span*width + (span-1)*gap', () => {
    // colSpan 3 at width 33, gap 8 → 3*33 + 2*8 = 115 (NOT 3*stride).
    expect(gridBoxToPx({ col: 0, colSpan: 3, row: 0, rowSpan: 1, z: 0 }, gapped).w).toBe(115);
  });
});

describe('pxToGridBox column clamp', () => {
  it('clamps col and colSpan to the grid when columns is given', () => {
    // A rect landing at col 23 span 4 on a 24-col grid clamps to col 23 span 1.
    const wide: GridGeometry = { colWidth: 100, colGap: 0, rowHeight: 50, rowGap: 0 };
    const box = pxToGridBox({ x: 2300, y: 0, w: 400, h: 50 }, wide, 0, undefined, 24);
    expect(box.col).toBe(23);
    expect(box.colSpan).toBe(1); // 24 - 23
  });

  it('leaves the box unclamped when columns is omitted', () => {
    const wide: GridGeometry = { colWidth: 100, colGap: 0, rowHeight: 50, rowGap: 0 };
    expect(pxToGridBox({ x: 2300, y: 0, w: 400, h: 50 }, wide, 0).colSpan).toBe(4);
  });
});

describe('setCanvasGrid', () => {
  const withCols = (columns: number): DashboardDefinition => ({
    ...def(),
    canvas: { grid: { columns, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [{ id: 'a', type: 'gauge', layout: { base: { col: 16, colSpan: 8, row: 0, rowSpan: 4, z: 0 } } }],
  });

  it('floors columns/rowHeight to >=1 integers', () => {
    expect(setCanvasGrid(withCols(24), { columns: 12.7 }).canvas.grid.columns).toBe(13);
    expect(setCanvasGrid(withCols(24), { columns: 0 }).canvas.grid.columns).toBe(1);
    expect(setCanvasGrid(withCols(24), { rowHeight: -5 }).canvas.grid.rowHeight).toBe(1);
  });

  it('clamps widgets back inside the grid when columns shrinks', () => {
    const next = setCanvasGrid(withCols(24), { columns: 12 });
    // widget at col 16 span 8 would overrun 12 cols → clamp col 11, span 1.
    expect(baseBox(next.widgets[0])).toEqual({ col: 11, colSpan: 1, row: 0, rowSpan: 4, z: 0 });
  });

  it('leaves widgets untouched when columns grows or is unchanged', () => {
    const d = withCols(24);
    expect(setCanvasGrid(d, { columns: 48 }).widgets[0]).toBe(d.widgets[0]);
    expect(setCanvasGrid(d, { gap: 4 }).widgets[0]).toBe(d.widgets[0]);
  });
});
