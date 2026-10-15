// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { isDirty, parseDashboardDefinition, serializeDefinition } from './definition';
import {
  addWidget,
  basePlacement,
  clampSpan,
  deleteWidget,
  gridPlacementToPx,
  moveWidget,
  pxToGridPlacement,
  resizeWidget,
  setCanvasGrid,
  setTitle,
  setWidgetPlacement,
  updateWidget,
  type GridGeometry,
} from './editor-model';
import type { DashboardDefinition, GridPlacement, Placement } from './types';

const at = (col: number, row: number, cols = 4, rows = 3): GridPlacement => ({ mode: 'grid', col, row, cols, rows });

function def(): DashboardDefinition {
  return {
    schemaVersion: 2,
    title: 'Test',
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [
      { id: 'a', type: 'gauge', layout: { base: at(0, 0) } },
      { id: 'b', type: 'label', layout: { base: at(6, 0) } },
    ],
  };
}

// Every transform must leave a definition the parser accepts: no grid overlap, no dangling host.
function reloads(d: DashboardDefinition) {
  expect(() => parseDashboardDefinition(JSON.parse(serializeDefinition(d)))).not.toThrow();
}

describe('setWidgetPlacement', () => {
  it('replaces only the target widget’s base placement', () => {
    const d = def();
    const next = setWidgetPlacement(d, 'a', at(0, 5, 3, 2));
    expect(basePlacement(next.widgets[0])).toEqual(at(0, 5, 3, 2));
    expect(next.widgets[1]).toBe(d.widgets[1]); // untouched
  });

  it('pushes a widget it lands on down, keeping that widget’s column and span', () => {
    const next = setWidgetPlacement(def(), 'a', at(5, 0, 4, 3)); // now covers b's columns 6..8
    expect(basePlacement(next.widgets[0])).toEqual(at(5, 0, 4, 3));
    expect(basePlacement(next.widgets[1])).toEqual(at(6, 3)); // straight down, below a
    reloads(next);
  });

  it('pushes a chain: a widget pushed onto another pushes that one too', () => {
    const d: DashboardDefinition = {
      ...def(),
      widgets: [
        { id: 'a', type: 'gauge', layout: { base: at(0, 0, 4, 2) } },
        { id: 'b', type: 'label', layout: { base: at(0, 2, 4, 2) } },
        { id: 'c', type: 'label', layout: { base: at(0, 4, 4, 2) } },
      ],
    };
    const next = setWidgetPlacement(d, 'a', at(0, 0, 4, 3));
    expect(next.widgets.map(basePlacement)).toEqual([at(0, 0, 4, 3), at(0, 3, 4, 2), at(0, 5, 4, 2)]);
    reloads(next);
  });
});

describe('deleteWidget', () => {
  it('removes the widget by id', () => {
    const next = deleteWidget(def(), 'a');
    expect(next.widgets.map((w) => w.id)).toEqual(['b']);
  });

  it('puts a widget that was placed relative to the deleted one back on the grid, so the board still loads', () => {
    const badge: Placement = { mode: 'relative', host: 'a', anchor: 'top-right', size: { cols: 1, rows: 1 } };
    const filler: Placement = { mode: 'relative', host: 'a', anchor: 'center', size: { fill: 'host' } };
    const d: DashboardDefinition = {
      ...def(),
      widgets: [...def().widgets, { id: 'badge', type: 'label', layout: { base: badge } }, { id: 'fill', type: 'label', layout: { base: filler } }],
    };
    const next = deleteWidget(d, 'a');
    expect(basePlacement(next.widgets.find((w) => w.id === 'badge')!)).toEqual(at(0, 0, 1, 1));
    // `fill` takes the deleted host's span.
    expect(basePlacement(next.widgets.find((w) => w.id === 'fill')!)).toMatchObject({ mode: 'grid', cols: 4, rows: 3 });
    reloads(next);
  });
});

// A board with a breakpoint override: `b` has its own `md` placement, `a` and `c` show their base
// ones at md. An edit to the base layout changes what `a` and `c` show at md too, so it must not
// leave them on top of `b` there -- the parser checks every breakpoint and would refuse the board.
describe('editing the base layout of a board with a breakpoint override', () => {
  function withOverride(): DashboardDefinition {
    return {
      ...def(),
      canvas: { ...def().canvas, breakpoints: { base: 0, md: 900 } },
      widgets: [
        { id: 'a', type: 'gauge', layout: { base: at(0, 0) } },
        { id: 'b', type: 'label', layout: { base: at(6, 0), md: at(0, 6) } },
        { id: 'c', type: 'label', layout: { base: at(0, 10, 2, 2) } },
      ],
    };
  }

  it('a move that is clear at base but lands on an override at md pushes it apart at md', () => {
    const next = setWidgetPlacement(withOverride(), 'a', at(0, 6));
    expect(basePlacement(next.widgets[0])).toEqual(at(0, 6));
    reloads(next);
  });

  it('an added widget that is clear at base but lands on an override at md', () => {
    // The first free slot at base is (0,3), under `a`, where `b` sits at md.
    const d = withOverride();
    d.widgets[1] = { ...d.widgets[1], layout: { base: at(6, 0), md: at(0, 3) } };
    const { definition } = addWidget(d, { id: 'label', defaults: {}, version: '1.0.0', optionsVersion: 1, sdkApiVersion: 1, layout: { defaultSpan: { cols: 12, rows: 2 } } });
    reloads(definition);
  });

  it('a relative widget re-homed on its host’s deletion that lands on an override at md', () => {
    const d = withOverride();
    // The first free 2x2 at base is (4,0), between `a` and `b`; `b` covers it at md.
    d.widgets[1] = { ...d.widgets[1], layout: { base: at(6, 0), md: at(4, 0, 8, 2) } };
    d.widgets.push({ id: 'badge', type: 'label', layout: { base: { mode: 'relative', host: 'c', anchor: 'top-left', size: { cols: 2, rows: 2 } } } });
    reloads(deleteWidget(d, 'c'));
  });
});

describe('updateWidget', () => {
  it('replaces the matching widget and leaves others untouched', () => {
    const d = def();
    const next = updateWidget(d, 'a', { id: 'a', type: 'latest-card', layout: { base: at(0, 0) }, options: { title: 'Renamed' } });
    expect(next.widgets[0]).toEqual({ id: 'a', type: 'latest-card', layout: { base: at(0, 0) }, options: { title: 'Renamed' } });
    expect(next.widgets[1]).toBe(d.widgets[1]); // untouched
  });
});

describe('addWidget', () => {
  const gauge = { id: 'gauge', version: '1.0.0', optionsVersion: 1, sdkApiVersion: 1, defaults: { title: 'Gauge' } } as const;

  it('appends a widget with a unique id and the descriptor defaults', () => {
    const d = def();
    const { definition, id } = addWidget(d, gauge);
    expect(definition.widgets).toHaveLength(3);
    const added = definition.widgets[2];
    expect(added.id).toBe(id);
    expect(added.id).not.toBe('a');
    expect(added.id).not.toBe('b');
    expect(added.type).toBe('gauge');
    expect(added.datasource).toBeUndefined();
    expect(added.options).toEqual({ title: 'Gauge' });
  });

  it('places it in the first free slot, scanning rows, never on top of another widget', () => {
    // a covers columns 0..3 and b 6..9 on rows 0..2: a 2-wide widget fits at column 4 of row 0.
    const narrow = addWidget(def(), { ...gauge, layout: { defaultSpan: { cols: 2, rows: 2 } } }).definition;
    expect(basePlacement(narrow.widgets[2])).toEqual(at(4, 0, 2, 2));
    // A 4-wide one does not fit beside them, so it goes below.
    const wide = addWidget(def(), gauge).definition; // the 4x2 fallback
    expect(basePlacement(wide.widgets[2])).toEqual(at(0, 3, 4, 2));
    reloads(narrow);
    reloads(wide);
  });

  it('is never wider than the grid', () => {
    const huge = addWidget(def(), { ...gauge, layout: { defaultSpan: { cols: 30, rows: 2 } } }).definition;
    expect(basePlacement(huge.widgets[2])).toMatchObject({ col: 0, cols: 12 });
  });

  it('takes its starting options from the descriptor, whatever the widget id', () => {
    const { definition } = addWidget(def(), {
      id: 'acme/tank-level',
      version: '1.2.0',
      optionsVersion: 3,
      sdkApiVersion: 1,
      defaults: { title: 'Tank', unit: '%' },
    });
    expect(definition.widgets[2].type).toBe('acme/tank-level');
    expect(definition.widgets[2].options).toEqual({ title: 'Tank', unit: '%' });
  });

  // The whole compatibility key is recorded, not just part of it: a reference without the SDK
  // generation could never be told apart from one written for a generation the host dropped.
  it('records the release, settings version and SDK generation it was authored against', () => {
    const ext = addWidget(def(), {
      id: 'acme/tank-level',
      version: '1.2.0',
      optionsVersion: 3,
      sdkApiVersion: 1,
      defaults: {},
    }).definition.widgets[2];
    expect(ext.widget).toEqual({ version: '1.2.0', optionsVersion: 3, sdkApiVersion: 1 });
    expect(addWidget(def(), gauge).definition.widgets[2].widget).toEqual({
      version: '1.0.0',
      optionsVersion: 1,
      sdkApiVersion: 1,
    });
  });

  it("places a widget at its descriptor's default span, and a layout-less one at the fallback 4x2", () => {
    const sized = addWidget(def(), { ...gauge, layout: { defaultSpan: { cols: 6, rows: 3 } } }).definition.widgets[2];
    expect(basePlacement(sized)).toMatchObject({ cols: 6, rows: 3 });
    const bare = addWidget(def(), gauge).definition.widgets[2];
    expect(basePlacement(bare)).toMatchObject({ cols: 4, rows: 2 });
  });

  it('gives each widget its own copy of the defaults', () => {
    const shared = { id: 'label', version: '1.0.0', optionsVersion: 1, sdkApiVersion: 1, defaults: { text: 'New label' } } as const;
    const first = addWidget(def(), shared).definition.widgets[2];
    expect(first.options).not.toBe(shared.defaults);
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

  it('ignores the conversion mark: it is never stored', () => {
    expect(isDirty({ ...def(), convertedFrom: 1 }, def())).toBe(false);
  });
});

// Square 100 px cells, no gutter: strides of 100 on both axes.
const geom: GridGeometry = { cell: 100, gutter: { x: 0, y: 0 } };

describe('pxToGridPlacement', () => {
  it('snaps pixels to cell lines and clamps', () => {
    // x 205 → col 2, w 290 → cols 3; y 120 → row 1, h 160 → rows 2.
    expect(pxToGridPlacement({ x: 205, y: 120, w: 290, h: 160 }, geom)).toEqual(at(2, 1, 3, 2));
    // negative/zero clamps to col,row>=0 and spans>=1.
    expect(pxToGridPlacement({ x: -20, y: -1, w: 2, h: 0 }, geom)).toEqual(at(0, 0, 1, 1));
  });

  it('clamps col and cols to the grid when columns is given, and not otherwise', () => {
    expect(pxToGridPlacement({ x: 1100, y: 0, w: 400, h: 100 }, geom, 12)).toMatchObject({ col: 11, cols: 1 });
    expect(pxToGridPlacement({ x: 1100, y: 0, w: 400, h: 100 }, geom).cols).toBe(4);
  });
});

describe('gridPlacementToPx', () => {
  it('renders a placement to a pixel rect', () => {
    expect(gridPlacementToPx(at(2, 2, 3, 3), geom)).toEqual({ x: 200, y: 200, w: 300, h: 300 });
  });

  // The two functions are inverses, exercised WITH a nonzero gutter so the `(span-1)*gutter`
  // term and the `+gutter` snap correction are pinned.
  it('round-trips through pxToGridPlacement with a gutter, and renders n*cell + (n-1)*gutter', () => {
    const gapped: GridGeometry = { cell: 33, gutter: { x: 8, y: 4 } };
    for (const p of [at(0, 0, 1, 1), at(4, 2, 3, 5), at(1, 1, 1, 1)]) {
      expect(pxToGridPlacement(gridPlacementToPx(p, gapped), gapped)).toEqual(p);
    }
    expect(gridPlacementToPx(at(0, 0, 3, 1), gapped).w).toBe(115); // 3*33 + 2*8, not 3 strides
  });
});

describe('setCanvasGrid', () => {
  const withWide = (): DashboardDefinition => ({
    ...def(),
    widgets: [
      { id: 'a', type: 'gauge', layout: { base: at(8, 0, 4, 2) } },
      { id: 'b', type: 'gauge', layout: { base: at(5, 0, 1, 2) } },
    ],
  });

  it('floors columns to an integer >= 1 and minCellSize to >= 1', () => {
    expect(setCanvasGrid(withWide(), { columns: 12.7 }).canvas.grid.columns).toBe(13);
    expect(setCanvasGrid(withWide(), { columns: 0 }).canvas.grid.columns).toBe(1);
    expect(setCanvasGrid(withWide(), { minCellSize: -5 }).canvas.grid.minCellSize).toBe(1);
  });

  it('clamps widgets back inside the grid when columns shrinks, and pushes down what the clamp makes collide', () => {
    const next = setCanvasGrid(withWide(), { columns: 6 });
    // a (8..12) clamps to column 5, span 1, onto b (5..6). Both now start at row 0, column 5,
    // so reading order cannot separate them and the earlier widget in the board keeps its place.
    expect(basePlacement(next.widgets[0])).toEqual(at(5, 0, 1, 2));
    expect(basePlacement(next.widgets[1])).toEqual(at(5, 2, 1, 2));
    reloads(next);
  });

  it('leaves widgets untouched when columns grows or another field changes', () => {
    const d = withWide();
    expect(setCanvasGrid(d, { columns: 24 }).widgets[0]).toBe(d.widgets[0]);
    expect(setCanvasGrid(d, { gutter: 4 }).widgets[0]).toBe(d.widgets[0]);
  });
});

describe('moveWidget', () => {
  it('moves the widget to the cell and pushes the one it lands on down, keeping its column and span', () => {
    const next = moveWidget(def(), 'a', { col: 5, row: 0 });
    expect(basePlacement(next.widgets[0])).toEqual(at(5, 0, 4, 3));
    expect(basePlacement(next.widgets[1])).toEqual(at(6, 3, 4, 3));
    reloads(next);
  });

  it('holds the footprint inside the columns and above row 0', () => {
    const right = moveWidget(def(), 'a', { col: 11, row: -3 });
    expect(basePlacement(right.widgets[0])).toEqual(at(8, 0, 4, 3)); // 12 columns - 4 wide
    expect(basePlacement(right.widgets[1])).toEqual(at(6, 3, 4, 3)); // now under it
    reloads(right);
  });

  it('moves into free space without disturbing anyone, and returns the same board for a no-op', () => {
    const d = def();
    const next = moveWidget(d, 'a', { col: 0, row: 6 });
    expect(basePlacement(next.widgets[0])).toEqual(at(0, 6, 4, 3));
    expect(next.widgets[1]).toBe(d.widgets[1]);
    expect(moveWidget(d, 'a', { col: 0, row: 0 })).toBe(d);
    expect(moveWidget(d, 'nope', { col: 1, row: 1 })).toBe(d);
  });
});

describe('resizeWidget', () => {
  const limits = { minSpan: { cols: 3, rows: 2 }, maxSpan: { cols: 6, rows: 4 } };

  it('stops at the widget maximum and minimum, cell by cell', () => {
    const big = resizeWidget(def(), 'a', { col: 0, row: 0, cols: 9, rows: 9 }, limits);
    expect(basePlacement(big.widgets[0])).toEqual(at(0, 0, 6, 4));
    const small = resizeWidget(def(), 'a', { col: 0, row: 0, cols: 1, rows: 1 }, limits);
    expect(basePlacement(small.widgets[0])).toEqual(at(0, 0, 3, 2));
  });

  it('pushes the neighbours it grows over down', () => {
    const next = resizeWidget(def(), 'a', { col: 0, row: 0, cols: 7, rows: 3 }, { maxSpan: { cols: 8 } });
    expect(basePlacement(next.widgets[0])).toEqual(at(0, 0, 7, 3));
    expect(basePlacement(next.widgets[1])).toEqual(at(6, 3, 4, 3));
    reloads(next);
  });

  it('keeps the opposite edge fixed when a left or top handle is clamped', () => {
    const d = setWidgetPlacement(def(), 'a', at(4, 4, 4, 3)); // right edge 8, bottom edge 7
    const next = resizeWidget(d, 'a', { col: 7, row: 6, cols: 1, rows: 1 }, limits, { fromLeft: true, fromTop: true });
    expect(basePlacement(next.widgets[0])).toEqual(at(5, 5, 3, 2)); // 8 - 3, 7 - 2
  });

  it('does not run past the right edge or let the left edge go below column 0', () => {
    const d = setWidgetPlacement(def(), 'a', at(9, 0, 3, 3));
    const right = resizeWidget(d, 'a', { col: 9, row: 0, cols: 6, rows: 3 }, undefined);
    expect(basePlacement(right.widgets[0])).toEqual(at(9, 0, 3, 3));
    const left = resizeWidget(def(), 'a', { col: -2, row: 0, cols: 6, rows: 3 }, undefined, { fromLeft: true });
    expect(basePlacement(left.widgets[0]).mode === 'grid' && (basePlacement(left.widgets[0]) as GridPlacement).col).toBe(0);
  });

  it('holds a pinned axis where its limits meet', () => {
    const pinned = { minSpan: { rows: 3 }, maxSpan: { rows: 3 } };
    const next = resizeWidget(def(), 'a', { col: 0, row: 0, cols: 5, rows: 8 }, pinned);
    expect(basePlacement(next.widgets[0])).toEqual(at(0, 0, 5, 3));
  });
});

describe('clampSpan', () => {
  it('is the identity without limits and rounds to whole cells', () => {
    expect(clampSpan({ cols: 3.4, rows: 2.6 }, undefined, 12)).toEqual({ cols: 3, rows: 3 });
  });
  it('lets the room to the right win over a minimum', () => {
    expect(clampSpan({ cols: 1, rows: 1 }, { minSpan: { cols: 5 } }, 2)).toEqual({ cols: 2, rows: 1 });
  });
});
