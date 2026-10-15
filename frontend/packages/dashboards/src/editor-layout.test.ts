// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor's size-enforcement transforms: live compaction, first-free-slot placement, the size
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
import { compactUp } from './grid';
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

describe('compaction', () => {
  const stack = () => board([w('a', at(0, 0)), w('b', at(0, 3)), w('c', at(0, 6))]);

  it('closes the space a drag leaves and keeps a pushed widget from jumping over the dragged one', () => {
    const next = moveWidget(stack(), 'a', { col: 0, row: 6 }, 12, { compact: true });
    // b falls into the vacated rows; c is pushed under a and cannot pass it.
    expect(rows(next)).toEqual({ a: at(0, 6), b: at(0, 0), c: at(0, 9) });
    reloads(next);
  });

  it('is off by default: the stored arrangement is only pushed apart', () => {
    const next = moveWidget(stack(), 'a', { col: 0, row: 6 });
    expect(rows(next)).toEqual({ a: at(0, 6), b: at(0, 3), c: at(0, 9) });
  });

  it('pulls a widget up into the room a neighbour left beside it', () => {
    const d = board([w('a', at(0, 0)), w('b', at(4, 0)), w('c', at(0, 3))]);
    const next = moveWidget(d, 'a', { col: 8, row: 0 }, 12, { compact: true });
    expect(rows(next).c).toEqual(at(0, 0));
    expect(rows(next).a).toEqual(at(8, 0));
  });

  it('a resize that grows pushes neighbours down, and shrinking lets them back up', () => {
    const d = board([w('a', at(0, 0)), w('b', at(0, 3))]);
    const grown = resizeWidget(d, 'a', { col: 0, row: 0, cols: 4, rows: 5 }, undefined, {}, 12, { compact: true });
    expect(rows(grown).b.row).toBe(5);
    const shrunk = resizeWidget(grown, 'a', { col: 0, row: 0, cols: 4, rows: 2 }, undefined, {}, 12, { compact: true });
    expect(rows(shrunk).b.row).toBe(2);
  });

  it('compactUp is deterministic, idempotent and never leaves an overlap', () => {
    const items = [
      { id: 'a', placement: at(0, 4, 6, 2) },
      { id: 'b', placement: at(3, 9, 6, 2) },
      { id: 'c', placement: at(8, 1, 4, 5) },
      { id: 'd', placement: at(0, 12, 12, 1) },
    ];
    const once = compactUp(items, ['b']);
    const again = compactUp(items.map((i) => ({ id: i.id, placement: once.get(i.id)! })), ['b']);
    for (const [id, p] of once) expect(again.get(id)).toEqual(p);
    expect(compactUp(items, ['b'])).toEqual(once);
    const list = [...once.values()];
    for (let i = 0; i < list.length; i++)
      for (let j = i + 1; j < list.length; j++) {
        const [p, q] = [list[i], list[j]];
        const overlap = p.col < q.col + q.cols && q.col < p.col + p.cols && p.row < q.row + q.rows && q.row < p.row + p.rows;
        expect(overlap).toBe(false);
      }
    expect(once.get('b')).toEqual(at(3, 9, 6, 2)); // the fixed one stays exactly where it was put
  });

  it('only the base layout is pulled together; a breakpoint arrangement is kept', () => {
    const d = board([
      w('a', at(0, 0)),
      { id: 'b', type: 'label', layout: { base: at(0, 3), md: at(0, 8) } },
    ]);
    d.canvas.breakpoints = { base: 0, md: 900 };
    const next = moveWidget(d, 'a', { col: 4, row: 0 }, 12, { compact: true });
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

  it('clamps every out-of-limit widget and re-packs, leaving nothing that blocks', () => {
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
    // Re-packed: `under` rises into the room `big` gave up.
    expect(rows(fixed).under.row).toBeLessThan(6);
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

  it('returns the same object when everything already fits', () => {
    const d = board([small('a', at(0, 0, 3, 3))]);
    expect(fitToLimits(d, limitsOf)).toBe(d);
  });
});
