// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor's size-enforcement transforms: displacement on edit, first-free-slot placement, the size
// readout, relative placement, fit-to-limits and the publish block. Pure; the console wires them.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition } from './definition';
import {
  addWidget,
  basePlacement,
  fitToLimits,
  formatSpan,
  moveWidget,
  placeOnGrid,
  placeRelative,
  resizeWidget,
  setRelativeAnchor,
  setRelativeOffset,
  snapOffset,
} from './editor-model';
import { gridFootprintsOverlap, pushAside } from './grid';
import { blockingLayoutFindings, lintLayout, type SpanLimits } from './layout-lint';
import type { DashboardDefinition, GridPlacement, WidgetInstance } from './types';

const at = (col: number, row: number, cols = 4, rows = 3): GridPlacement => ({ mode: 'grid', col, row, cols, rows });

function board(widgets: WidgetInstance[], columns = 12): DashboardDefinition {
  return {
    schemaVersion: 2,
    title: 'T',
    canvas: { grid: { columns, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets,
  };
}
const w = (id: string, p: GridPlacement | WidgetInstance['layout']['base']): WidgetInstance => ({
  id,
  type: 'label',
  layout: { base: p },
});
const rows = (d: DashboardDefinition) =>
  Object.fromEntries(d.widgets.map((x) => [x.id, basePlacement(x)])) as Record<string, GridPlacement>;
function reloads(d: DashboardDefinition) {
  expect(() => parseDashboardDefinition(JSON.parse(serializeDefinition(d)))).not.toThrow();
}
const gaugeLike = { id: 'gauge', version: '1.0.0', optionsVersion: 1, sdkApiVersion: 1, defaults: {} } as const;

describe('addWidget: first free slot', () => {
  it('goes below a full first row, at column 0, never stacked at 0,0', () => {
    const d = board([w('a', at(0, 0)), w('b', at(4, 0)), w('c', at(8, 0))]);
    const { definition, id } = addWidget(d, { ...gaugeLike, layout: { defaultSpan: { cols: 4, rows: 2 } } });
    expect(rows(definition)[id]).toEqual(at(0, 3, 4, 2));
    reloads(definition);
  });

  it('skips a gap narrower than the widget and takes the next row', () => {
    // Columns 5 and 11 are free but only one wide; a 3-wide widget cannot use them.
    const d = board([w('a', at(0, 0, 5, 2)), w('b', at(6, 0, 5, 2))]);
    const { definition, id } = addWidget(d, { ...gaugeLike, layout: { defaultSpan: { cols: 3, rows: 2 } } });
    expect(rows(definition)[id]).toEqual(at(0, 2, 3, 2));
  });

  it('uses the space beside a tall widget before opening a new row', () => {
    const d = board([w('tall', at(0, 0, 4, 6)), w('b', at(4, 0, 8, 2))]);
    const { definition, id } = addWidget(d, { ...gaugeLike, layout: { defaultSpan: { cols: 4, rows: 2 } } });
    expect(rows(definition)[id]).toEqual(at(4, 2, 4, 2));
  });

  it('a second add does not land on the first', () => {
    const one = addWidget(board([]), { ...gaugeLike, layout: { defaultSpan: { cols: 6, rows: 3 } } });
    const two = addWidget(one.definition, { ...gaugeLike, layout: { defaultSpan: { cols: 6, rows: 3 } } });
    expect(rows(two.definition)[one.id]).toEqual(at(0, 0, 6, 3));
    expect(rows(two.definition)[two.id]).toEqual(at(6, 0, 6, 3));
  });
});

describe('an edit moves only what it displaces', () => {
  const stack = () => board([w('a', at(0, 0)), w('b', at(0, 3)), w('c', at(0, 6))]);

  it('swaps a widget dropped onto a neighbour: the neighbour takes the room the dragged one left', () => {
    const next = moveWidget(stack(), 'a', { col: 0, row: 3 }, 12, { nearest: true });
    expect(rows(next)).toEqual({ a: at(0, 3), b: at(0, 0), c: at(0, 6) });
    reloads(next);
  });

  it('the dragged widget lands where it is dropped, and a gap it leaves stays open', () => {
    const next = moveWidget(stack(), 'a', { col: 0, row: 6 }, 12, { nearest: true });
    // b was not in the way and does not move; c, covered, goes to its nearest free row (down 3,
    // nearer than the 6 up to the vacated rows).
    expect(rows(next)).toEqual({ a: at(0, 6), b: at(0, 3), c: at(0, 9) });
  });

  it('never pulls an undisplaced widget up into a gap the author left', () => {
    // A deliberate one-row gap above c, and an empty column 8.. beside a.
    const d = board([w('a', at(0, 0)), w('b', at(4, 0)), w('c', at(0, 4))]);
    const next = moveWidget(d, 'a', { col: 8, row: 0 }, 12, { nearest: true });
    expect(rows(next)).toEqual({ a: at(8, 0), b: at(4, 0), c: at(0, 4) });
  });

  it('a resize that grows displaces neighbours; shrinking leaves them where they are', () => {
    const d = board([w('a', at(0, 0)), w('b', at(0, 3))]);
    const grown = resizeWidget(d, 'a', { col: 0, row: 0, cols: 4, rows: 5 }, undefined, {}, 12, { nearest: true });
    expect(rows(grown).b.row).toBe(5);
    const shrunk = resizeWidget(grown, 'a', { col: 0, row: 0, cols: 4, rows: 2 }, undefined, {}, 12, { nearest: true });
    expect(rows(shrunk).b.row).toBe(5);
  });

  it('a displaced widget takes room above only when its whole footprint fits there', () => {
    // a is dropped over b. Up, the rows a left are free only where a was: at 8 columns wide b
    // would cover d beside it, so b goes down instead and d stays put.
    const d = board([w('a', at(0, 0, 4, 2)), w('d', at(4, 0, 4, 2)), w('b', at(0, 2, 8, 2))]);
    const next = moveWidget(d, 'a', { col: 0, row: 2 }, 12, { nearest: true });
    expect(rows(next).d).toEqual(at(4, 0, 4, 2));
    expect(rows(next).b.row).toBe(4);
    reloads(next);
  });

  it('pushAside is deterministic, the identity on a clear board, and never leaves an overlap', () => {
    let seed = 7;
    const rand = (n: number) => {
      seed = (seed * 1103515245 + 12345) % 2147483648;
      return seed % n;
    };
    for (let trial = 0; trial < 300; trial++) {
      const items: { id: string; placement: GridPlacement }[] = [];
      for (let i = 0; i < 6; i++) {
        const cols = 1 + rand(6);
        const p = at(rand(12 - cols + 1), rand(12), cols, 1 + rand(4));
        if (items.every((o) => !gridFootprintsOverlap(p, o.placement))) items.push({ id: `w${i}`, placement: p });
      }
      // A clear board is returned as it is.
      const same = pushAside(items);
      for (const i of items) expect(same.get(i.id)).toBe(i.placement);
      // Drop one of them somewhere else.
      const moved = items[rand(items.length)];
      const dropped = { ...moved.placement, col: rand(12 - moved.placement.cols + 1), row: rand(10) };
      const input = items.map((i) => (i === moved ? { id: i.id, placement: dropped } : i));
      const out = pushAside(input, [moved.id]);
      expect(pushAside(input, [moved.id])).toEqual(out);
      expect(out.get(moved.id)).toEqual(dropped);
      const list = [...out.values()];
      for (let i = 0; i < list.length; i++)
        for (let j = i + 1; j < list.length; j++) expect(gridFootprintsOverlap(list[i], list[j])).toBe(false);
      // Only a widget the drop covered can have moved, when nothing it covered moved down onto another.
      const covered = input.filter((i) => i.id !== moved.id && gridFootprintsOverlap(i.placement, dropped));
      if (covered.every((c) => out.get(c.id)!.row < c.placement.row))
        for (const i of input) if (i.id !== moved.id && !covered.includes(i)) expect(out.get(i.id)).toBe(i.placement);
    }
  });

  it('only the base layout is rearranged; a breakpoint arrangement is kept', () => {
    const d = board([
      w('a', at(0, 0)),
      { id: 'b', type: 'label', layout: { base: at(0, 3), md: at(0, 8) } },
    ]);
    d.canvas.breakpoints = { base: 0, md: 900 };
    const next = moveWidget(d, 'a', { col: 0, row: 3 }, 12, { nearest: true });
    expect(next.widgets[1].layout.md).toEqual(at(0, 8));
    expect(basePlacement(next.widgets[1])).toEqual(at(0, 0));
  });
});

describe('size readout', () => {
  it('reads columns by rows with the multiplication sign', () => {
    expect(formatSpan({ cols: 2, rows: 3 })).toBe('2×3');
    expect(formatSpan({ cols: 12, rows: 1 })).toBe('12×1');
  });
});

describe('relative placement', () => {
  const hostBoard = () => board([w('host', at(2, 2, 4, 3)), w('badge', at(8, 0, 2, 2))]);

  it('makes a widget relative to a host, keeping its size and sitting at the anchor', () => {
    const next = placeRelative(hostBoard(), 'badge', 'host', 'top-right');
    expect(basePlacement(next.widgets[1])).toEqual({
      mode: 'relative',
      host: 'host',
      anchor: 'top-right',
      size: { cols: 2, rows: 2 },
    });
    reloads(next);
  });

  it('refuses itself, a missing host and a host that is itself relative', () => {
    const d = hostBoard();
    expect(placeRelative(d, 'badge', 'badge', 'center')).toBe(d);
    expect(placeRelative(d, 'badge', 'nope', 'center')).toBe(d);
    const chained = placeRelative(d, 'badge', 'host', 'center');
    chained.widgets.push(w('third', at(0, 8)));
    expect(placeRelative(chained, 'third', 'badge', 'center')).toBe(chained);
  });

  it('changes the anchor and brings a relative widget back to the grid in the first free slot', () => {
    const rel = placeRelative(hostBoard(), 'badge', 'host', 'top-right');
    const moved = setRelativeAnchor(rel, 'badge', 'bottom-left');
    expect((basePlacement(moved.widgets[1]) as { anchor: string }).anchor).toBe('bottom-left');
    const back = placeOnGrid(moved, 'badge');
    expect(rows(back).badge).toEqual(at(0, 0, 2, 2));
    reloads(back);
  });

  it('snaps an offset to whole cells, and to quarter cells with Shift', () => {
    expect(snapOffset(0.4)).toBe(0);
    expect(snapOffset(0.6)).toBe(1);
    expect(snapOffset(-1.4)).toBe(-1);
    expect(snapOffset(0.4, true)).toBe(0.5);
    expect(snapOffset(0.13, true)).toBe(0.25);
    expect(Object.is(snapOffset(-0.1), -0)).toBe(false);
    const rel = placeRelative(hostBoard(), 'badge', 'host', 'top-left');
    const whole = setRelativeOffset(rel, 'badge', { x: 1.4, y: -0.6 });
    expect((basePlacement(whole.widgets[1]) as { offset: unknown }).offset).toEqual({ x: 1, y: -1 });
    const fine = setRelativeOffset(rel, 'badge', { x: 1.4, y: -0.6 }, { fine: true });
    expect((basePlacement(fine.widgets[1]) as { offset: unknown }).offset).toEqual({ x: 1.5, y: -0.5 });
  });

  it('stores a zero offset as absent and ignores a grid widget', () => {
    const rel = setRelativeOffset(placeRelative(hostBoard(), 'badge', 'host', 'top-left'), 'badge', { x: 2, y: 0 });
    const zero = setRelativeOffset(rel, 'badge', { x: 0.2, y: 0.1 });
    expect('offset' in basePlacement(zero.widgets[1])).toBe(false);
    const d = hostBoard();
    expect(setRelativeOffset(d, 'host', { x: 3, y: 3 })).toBe(d);
  });

  it('keeps a dragged relative widget on the canvas at the live cell size', () => {
    const rel = placeRelative(hostBoard(), 'badge', 'host', 'top-left');
    const bounds = { cell: 56, gutter: { x: 8, y: 8 }, columns: 12 };
    const left = setRelativeOffset(rel, 'badge', { x: -20, y: -20 }, { bounds });
    // Host is at column 2 / row 2: the box may go no further left than column 0, no higher than row 0.
    expect((basePlacement(left.widgets[1]) as { offset: unknown }).offset).toEqual({ x: -2, y: -2 });
    const right = setRelativeOffset(rel, 'badge', { x: 40, y: 0 }, { bounds });
    const off = (basePlacement(right.widgets[1]) as { offset: { x: number } }).offset.x;
    expect(off).toBeLessThan(40);
    expect(blockingLayoutFindings(lintLayout(right, { columns: 12, spanLimitsOf: () => ({}) }))).toEqual([]);
  });
});

describe('fit to limits and the publish block', () => {
  const limits: Record<string, SpanLimits> = {
    label: { minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 4, rows: 3 } },
  };
  const limitsOf = (x: WidgetInstance) => limits[x.type] ?? {};
  const small = (id: string, p: WidgetInstance['layout']['base']): WidgetInstance => ({ id, type: 'label', layout: { base: p } });
  const options = { columns: 12, spanLimitsOf: limitsOf };
  const codes = (d: DashboardDefinition) => blockingLayoutFindings(lintLayout(d, options)).map((f) => f.code);

  it('blocks span-out-of-limits', () => {
    expect(codes(board([small('a', at(0, 0, 8, 3))]))).toEqual(['span-out-of-limits']);
    expect(codes(board([small('a', at(0, 0, 1, 1))]))).toEqual(['span-out-of-limits']);
  });

  it('blocks overrun-columns', () => {
    expect(codes(board([small('a', at(10, 0, 4, 3))]))).toContain('overrun-columns');
  });

  it('blocks relative-off-canvas', () => {
    const d = board([
      small('h', at(0, 0, 4, 3)),
      small('r', { mode: 'relative', host: 'h', anchor: 'top-left', offset: { x: -5, y: 0 }, size: { cols: 2, rows: 2 } }),
    ]);
    expect(codes(d)).toEqual(['relative-off-canvas']);
  });

  it('does not block on advice', () => {
    const d = board([w('a', at(0, 0))]);
    const findings = lintLayout(d, { columns: 12, spanLimitsOf: () => undefined });
    expect(findings.map((f) => f.code)).toEqual(['no-layout-declared']);
    expect(blockingLayoutFindings(findings)).toEqual([]);
  });

  it('clamps every out-of-limit widget, leaving nothing that blocks', () => {
    const d = board([
      small('big', at(0, 0, 8, 6)),
      small('tiny', at(8, 0, 1, 1)),
      small('edge', at(10, 4, 4, 3)),
      small('under', at(0, 6, 4, 3)),
    ]);
    const fixed = fitToLimits(d, limitsOf);
    expect(rows(fixed).big).toMatchObject({ col: 0, cols: 4, rows: 3 });
    expect(rows(fixed).tiny).toMatchObject({ cols: 2, rows: 2 });
    expect(rows(fixed).edge.col + rows(fixed).edge.cols).toBeLessThanOrEqual(12);
    expect(codes(fixed)).toEqual([]);
    reloads(fixed);
    // Nothing is pulled up: `under` keeps its row, and the room `big` gave up stays open.
    expect(rows(fixed).under).toEqual(at(0, 6, 4, 3));
  });

  it('brings an off-canvas relative widget back to its anchor, and fixes a breakpoint entry', () => {
    const d = board([
      small('h', at(0, 0, 4, 3)),
      {
        id: 'r',
        type: 'label',
        layout: {
          base: { mode: 'relative', host: 'h', anchor: 'top-left', offset: { x: -5, y: 0 }, size: { cols: 9, rows: 2 } },
        },
      },
    ]);
    const fixed = fitToLimits(d, limitsOf);
    expect(codes(fixed)).toEqual([]);
  });

  it('brings a relative widget with NO offset back on the canvas (a wide badge on a narrow host)', () => {
    // Anchored top-right of a one-column host at column 0, a three-column badge hangs off the left
    // edge with no offset to drop: what placeRelative produces when such a host is picked.
    const d = board([
      { id: 'h', type: 'free', layout: { base: at(0, 0, 1, 2) } },
      { id: 'r', type: 'free', layout: { base: { mode: 'relative', host: 'h', anchor: 'top-right', size: { cols: 3, rows: 1 } } } },
    ]);
    expect(codes(d)).toEqual(['relative-off-canvas']);
    const fixed = fitToLimits(d, limitsOf);
    expect(fixed).not.toBe(d);
    expect(codes(fixed)).toEqual([]);
    reloads(fixed);
  });

  it('keeps as much of an off-canvas offset as the canvas allows', () => {
    const d = board([
      { id: 'h', type: 'free', layout: { base: at(2, 0, 4, 3) } },
      {
        id: 'r',
        type: 'free',
        layout: {
          base: { mode: 'relative', host: 'h', anchor: 'top-left', offset: { x: -5, y: 0.5 }, size: { cols: 1, rows: 1 } },
        },
      },
    ]);
    const fixed = fitToLimits(d, limitsOf);
    expect((basePlacement(fixed.widgets[1]) as { offset: unknown }).offset).toEqual({ x: -2, y: 0.5 });
  });

  it('fixes a relative widget off the canvas only at a narrower breakpoint it INHERITS base at', () => {
    const d = board([
      { id: 'h', type: 'free', layout: { base: at(2, 0, 4, 2) } },
      {
        id: 'r',
        type: 'free',
        layout: {
          base: { mode: 'relative', host: 'h', anchor: 'top-right', self: 'top-left', size: { cols: 2, rows: 1 }, offset: { x: 2, y: 0 } },
        },
      },
    ]);
    // On 12 columns the badge ends at column 10; on 8 it runs off the right edge.
    d.canvas.breakpoints = { base: 0, sm: { minWidth: 400, columns: 8 } };
    expect(blockingLayoutFindings(lintLayout(d, options)).map((f) => [f.code, f.breakpoint])).toEqual([
      ['relative-off-canvas', 'sm'],
    ]);
    const fixed = fitToLimits(d, limitsOf);
    expect(blockingLayoutFindings(lintLayout(fixed, options))).toEqual([]);
    // Base was fine and is left alone; the fix is written for the breakpoint.
    expect(basePlacement(fixed.widgets[1])).toEqual(basePlacement(d.widgets[1]));
    expect(fixed.widgets[1].layout.sm).toBeDefined();
    reloads(fixed);
  });

  it("gives a `fill` widget an explicit size when its host is outside the widget's own limits", () => {
    const d = board([
      { id: 'h', type: 'free', layout: { base: at(0, 0, 8, 6) } },
      { id: 'r', type: 'label', layout: { base: { mode: 'relative', host: 'h', anchor: 'center', size: { fill: 'host' } } } },
    ]);
    expect(codes(d)).toEqual(['span-out-of-limits']);
    const fixed = fitToLimits(d, limitsOf);
    expect(codes(fixed)).toEqual([]);
    expect((basePlacement(fixed.widgets[1]) as { size: unknown }).size).toEqual({ cols: 4, rows: 3 });
  });

  it('repairs against every cell size the lint measures, not only the smallest', () => {
    // Hung by its left edge from the host's right edge, an 11-column box at x = -10.9 fits at
    // 56 px cells but runs off the right edge at very large ones, where the fixed (here wide)
    // gutter weighs less. Measured at the smallest cell alone, -10.9 would be kept.
    const d = board([
      { id: 'h', type: 'free', layout: { base: at(0, 0, 12, 2) } },
      {
        id: 'r',
        type: 'free',
        layout: {
          base: {
            mode: 'relative',
            host: 'h',
            anchor: 'top-right',
            self: 'top-left',
            offset: { x: -10.9, y: 0 },
            size: { cols: 11, rows: 1 },
          },
        },
      },
    ]);
    d.canvas.grid.gutter = 40;
    expect(codes(d)).toEqual(['relative-off-canvas']);
    const fixed = fitToLimits(d, limitsOf);
    expect((basePlacement(fixed.widgets[1]) as { offset: unknown }).offset).toEqual({ x: -11, y: 0 });
    expect(codes(fixed)).toEqual([]);
  });

  it('puts a relative widget on the grid when no offset keeps it on the canvas at every cell size', () => {
    // As wide as the canvas, with its left edge hung on the host's right edge: the gutter makes the
    // offset that fits small cells miss at large ones, so no single offset is on the canvas.
    const d = board([
      { id: 'h', type: 'free', layout: { base: at(0, 0, 12, 2) } },
      {
        id: 'r',
        type: 'free',
        layout: { base: { mode: 'relative', host: 'h', anchor: 'top-right', self: 'top-left', size: { cols: 12, rows: 1 } } },
      },
    ]);
    const fixed = fitToLimits(d, limitsOf);
    expect(basePlacement(fixed.widgets[1])).toEqual(at(0, 2, 12, 1));
    expect(codes(fixed)).toEqual([]);
    reloads(fixed);
  });

  it('always leaves nothing that blocks, whatever the board (seeded)', () => {
    let seed = 11;
    const rand = (n: number) => {
      seed = (seed * 1103515245 + 12345) % 2147483648;
      return seed % n;
    };
    const anchors = ['top-left', 'top', 'top-right', 'left', 'center', 'right', 'bottom-left', 'bottom', 'bottom-right'] as const;
    const types = ['label', 'free'];
    let checked = 0;
    for (let trial = 0; trial < 400; trial++) {
      const widgets: WidgetInstance[] = [];
      const grids: GridPlacement[] = [];
      for (let i = 0; i < 4; i++) {
        const p = at(rand(12), rand(10), 1 + rand(14), 1 + rand(7));
        if (grids.some((g) => gridFootprintsOverlap(g, p))) continue;
        grids.push(p);
        widgets.push({ id: `g${i}`, type: types[rand(2)], layout: { base: p } });
      }
      if (widgets.length === 0) continue;
      const hosts = widgets.map((x) => x.id);
      for (let i = 0; i < 3; i++) {
        const size = rand(4) === 0 ? ({ fill: 'host' } as const) : { cols: 1 + rand(14), rows: 1 + rand(5) };
        widgets.push({
          id: `r${i}`,
          type: types[rand(2)],
          layout: {
            base: {
              mode: 'relative',
              host: hosts[rand(hosts.length)],
              anchor: anchors[rand(9)],
              ...(rand(2) ? { self: anchors[rand(9)] } : {}),
              ...(rand(2) ? { offset: { x: (rand(41) - 20) / 4, y: (rand(41) - 20) / 4 } } : {}),
              size,
            },
          },
        });
      }
      const d = board(widgets);
      if (rand(2)) {
        d.canvas.breakpoints = { base: 0, sm: { minWidth: 400, columns: 4 + rand(6) } };
        // One grid widget gets its own entry there, below everything else.
        widgets[0].layout = { ...widgets[0].layout, sm: at(rand(6), 30 + rand(4), 1 + rand(8), 1 + rand(3)) };
      }
      const fixed = fitToLimits(d, limitsOf);
      expect(blockingLayoutFindings(lintLayout(fixed, options)), JSON.stringify(d)).toEqual([]);
      reloads(fixed);
      checked++;
    }
    expect(checked).toBeGreaterThan(300);
  });

  it('returns the same object when everything already fits', () => {
    const d = board([small('a', at(0, 0, 3, 3))]);
    expect(fitToLimits(d, limitsOf)).toBe(d);
  });
});
