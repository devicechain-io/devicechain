// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Placement modes, frames and the per-breakpoint column count, as the parser reads them, and the
// three refusals the format rests on. Each refusal is pinned by a deliberately broken document
// (the negative controls): an overlapping grid pair, a relative widget with no host, and a
// relative chain deeper than one level must each fail to parse, and the nearest legal neighbour
// of each must parse, so a check that refused everything would fail here too.

import { describe, expect, it } from 'vitest';

import { DashboardDefinitionError, parseDashboardDefinition, serializeDefinition } from './definition';
import { layoutRects, resolveFrame } from './grid';

const grid = (col: number, row: number, cols = 2, rows = 2) => ({ mode: 'grid', col, row, cols, rows });

function doc(widgets: unknown[], canvas: Record<string, unknown> = {}) {
  return { schemaVersion: 2, title: 't', canvas, widgets };
}

const label = (id: string, base: unknown, extra: Record<string, unknown> = {}) => ({
  id,
  type: 'label',
  layout: { base },
  options: { text: id },
  ...extra,
});

describe('grid placements', () => {
  it('parses a grid placement, and reads a placement with no mode as one', () => {
    const def = parseDashboardDefinition(doc([label('a', grid(0, 0)), label('b', { col: 2, row: 0, cols: 3, rows: 1 })]));
    expect(def.widgets[0].layout.base).toEqual({ mode: 'grid', col: 0, row: 0, cols: 2, rows: 2 });
    expect(def.widgets[1].layout.base).toEqual({ mode: 'grid', col: 2, row: 0, cols: 3, rows: 1 });
  });

  it('refuses a grid placement whose span is missing rather than guessing one', () => {
    expect(() => parseDashboardDefinition(doc([label('a', { mode: 'grid', col: 0, row: 0, cols: 2 })]))).toThrow(
      /widgets\[0\]\.layout\.base\.rows is undefined/,
    );
  });

  // NEGATIVE CONTROL: the overlap refusal.
  it('refuses two grid footprints that overlap, naming both', () => {
    const overlapping = doc([label('a', grid(0, 0, 3, 3)), label('b', grid(2, 2, 2, 2))]);
    expect(() => parseDashboardDefinition(overlapping)).toThrow(DashboardDefinitionError);
    expect(() => parseDashboardDefinition(overlapping)).toThrow(/'a' and 'b' overlap on the grid/);
  });

  it('accepts footprints that only touch along an edge', () => {
    expect(() => parseDashboardDefinition(doc([label('a', grid(0, 0, 3, 3)), label('b', grid(3, 0, 2, 2)), label('c', grid(0, 3))]))).not.toThrow();
  });

  it('checks the arrangement every breakpoint shows, inherited placements included', () => {
    // At `md`, a moves onto b, which shows its base placement there.
    const atMd = doc(
      [{ ...label('a', grid(0, 0)), layout: { base: grid(0, 0), md: grid(4, 0) } }, label('b', grid(4, 0))],
      { breakpoints: { base: 0, md: 900 } },
    );
    expect(() => parseDashboardDefinition(atMd)).toThrow(/overlap on the grid at breakpoint 'md'/);
  });

  it('refuses a reserved placement mode by name, and an unknown one', () => {
    expect(() => parseDashboardDefinition(doc([label('a', { mode: 'pinned', edge: 'top' })]))).toThrow(
      /mode 'pinned' is reserved and not supported by this release/,
    );
    expect(() => parseDashboardDefinition(doc([label('a', { mode: 'sticky' })]))).toThrow(/reserved/);
    expect(() => parseDashboardDefinition(doc([label('a', { mode: 'absolute' })]))).toThrow(/expected 'grid' or 'relative'/);
  });
});

describe('relative placements', () => {
  const badge = (host: string, extra: Record<string, unknown> = {}) => ({
    mode: 'relative',
    host,
    anchor: 'top-right',
    offset: { x: -0.25, y: 0.25 },
    size: { cols: 1, rows: 1 },
    ...extra,
  });

  it('parses a relative placement over its host without calling it an overlap', () => {
    const def = parseDashboardDefinition(doc([label('map', grid(0, 0, 4, 3)), label('badge', badge('map', { layer: 2 }))]));
    expect(def.widgets[1].layout.base).toEqual({
      mode: 'relative',
      host: 'map',
      anchor: 'top-right',
      offset: { x: -0.25, y: 0.25 },
      size: { cols: 1, rows: 1 },
      layer: 2,
    });
    // ...and it round-trips.
    expect(parseDashboardDefinition(JSON.parse(serializeDefinition(def)))).toEqual(def);
  });

  // NEGATIVE CONTROL: the missing host.
  it('refuses a relative widget whose host does not exist', () => {
    expect(() => parseDashboardDefinition(doc([label('a', grid(0, 0)), label('badge', badge('nope'))]))).toThrow(
      /'badge' is placed relative to 'nope', which does not exist/,
    );
  });

  it('refuses a widget placed relative to itself', () => {
    expect(() => parseDashboardDefinition(doc([label('badge', badge('badge'))]))).toThrow(/relative to itself/);
  });

  // NEGATIVE CONTROL: the chain depth. One level of relative-on-relative is allowed (a caption
  // under a badge on a map); a second is not.
  it('allows one level of relative-on-relative and refuses a deeper chain', () => {
    const one = doc([label('map', grid(0, 0, 4, 3)), label('badge', badge('map')), label('caption', badge('badge'))]);
    expect(() => parseDashboardDefinition(one)).not.toThrow();
    const two = doc([
      label('map', grid(0, 0, 4, 3)),
      label('badge', badge('map')),
      label('caption', badge('badge')),
      label('note', badge('caption')),
    ]);
    expect(() => parseDashboardDefinition(two)).toThrow(/'note' is placed relative to 'caption', which is relative to another relative widget/);
  });

  it('refuses a malformed anchor, offset, size or layer', () => {
    const bad = (extra: Record<string, unknown>) => () =>
      parseDashboardDefinition(doc([label('map', grid(0, 0, 4, 3)), label('b', badge('map', extra))]));
    expect(bad({ anchor: 'middle' })).toThrow(/anchor is "middle"/);
    expect(bad({ self: 'up' })).toThrow(/self is "up"/);
    expect(bad({ offset: { x: 1 } })).toThrow(/offset/);
    expect(bad({ size: { cols: 2 } })).toThrow(/size\.rows/);
    expect(bad({ size: 'host' })).toThrow(/size is "host"/);
    expect(bad({ layer: 1.5 })).toThrow(/layer/);
    expect(bad({ size: { fill: 'host' } })).not.toThrow();
  });

  it('lands a corner badge a quarter pitch inside its host at every cell size', () => {
    const def = parseDashboardDefinition(doc([label('map', grid(1, 0, 4, 3)), label('badge', badge('map', { self: 'top-right' }))]));
    for (const cell of [64, 120]) {
      const rects = layoutRects(def.widgets, 'base', cell, { x: 0, y: 0 });
      const map = rects.get('map')!;
      const b = rects.get('badge')!;
      expect(b.x + b.w).toBeCloseTo(map.x + map.w - cell / 4);
      expect(b.y).toBeCloseTo(map.y + cell / 4);
      expect(b.w).toBe(cell);
    }
  });

  it('places a caption beside its host with anchor bottom and self top', () => {
    const def = parseDashboardDefinition(
      doc([label('chart', grid(0, 0, 4, 2)), label('cap', badge('chart', { anchor: 'bottom', self: 'top', offset: undefined, size: { cols: 2, rows: 1 } }))]),
    );
    const rects = layoutRects(def.widgets, 'base', 100, { x: 10, y: 10 });
    const chart = rects.get('chart')!;
    const cap = rects.get('cap')!;
    expect(cap.y).toBe(chart.y + chart.h);
    expect(cap.x + cap.w / 2).toBe(chart.x + chart.w / 2);
  });
});

describe('breakpoints with their own column count', () => {
  it('parses {minWidth, columns} and keeps a bare min width', () => {
    const def = parseDashboardDefinition(doc([], { breakpoints: { base: 0, phone: { minWidth: 0, columns: 4 }, wide: 1600 } }));
    expect(def.canvas.breakpoints).toEqual({ base: 0, phone: { minWidth: 0, columns: 4 }, wide: 1600 });
  });

  it('refuses a base breakpoint in a column count other than the grid', () => {
    expect(() => parseDashboardDefinition(doc([], { grid: { columns: 12 }, breakpoints: { base: { minWidth: 0, columns: 8 } } }))).toThrow(
      /the base layout is in canvas\.grid\.columns/,
    );
  });
});

describe('frames', () => {
  it('parses a canvas default and a widget override, tokens only', () => {
    const def = parseDashboardDefinition(
      doc([label('a', grid(0, 0), { frame: { variant: 'none', border: { color: 'sev-critical' } } })], {
        frame: { variant: 'flat', padding: 'space-2', background: 'muted', header: 'show', border: { width: 2, radius: 'lg' } },
      }),
    );
    expect(def.canvas.frame).toEqual({
      variant: 'flat',
      border: { width: 2, radius: 'lg' },
      padding: 'space-2',
      background: 'muted',
      header: 'show',
    });
    expect(def.widgets[0].frame).toEqual({ variant: 'none', border: { color: 'sev-critical' } });
    expect(parseDashboardDefinition(JSON.parse(serializeDefinition(def)))).toEqual(def);
  });

  it('refuses a free colour, an unknown radius, a border wider than 3 and a px padding', () => {
    const with_ = (frame: unknown) => () => parseDashboardDefinition(doc([label('a', grid(0, 0), { frame })]));
    expect(with_({ border: { color: '#f00' } })).toThrow(/widgets\[0\]\.frame\.border\.color is "#f00"/);
    expect(with_({ background: 'red' })).toThrow(/frame\.background is "red"/);
    expect(with_({ border: { radius: 'xl' } })).toThrow(/radius/);
    expect(with_({ border: { width: 4 } })).toThrow(/width/);
    expect(with_({ padding: 12 })).toThrow(/padding/);
    expect(with_({ variant: 'glass' })).toThrow(/variant/);
    expect(() => parseDashboardDefinition(doc([], { frame: { border: { color: 'rgb(0,0,0)' } } }))).toThrow(/canvas\.frame\.border\.color/);
  });

  it('resolves a widget frame field by field over the canvas default, the variant preset under both', () => {
    expect(resolveFrame(undefined, undefined)).toEqual({
      variant: 'card',
      border: { width: 1, radius: 'md', color: 'border' },
      padding: 'space-3',
      background: 'card',
      header: 'auto',
    });
    // The widget keeps the canvas's border width and changes only its colour.
    expect(resolveFrame({ border: { width: 3 } }, { border: { color: 'sev-major' } }).border).toEqual({
      width: 3,
      radius: 'md',
      color: 'sev-major',
    });
    // A frameless widget on a card board: no border, transparent, no header.
    expect(resolveFrame({ variant: 'card' }, { variant: 'none' })).toMatchObject({
      border: { width: 0 },
      background: 'transparent',
      header: 'hide',
    });
    // An explicit field beats the preset of the variant the merge picked.
    expect(resolveFrame({ variant: 'none' }, { header: 'show' }).header).toBe('show');
  });
});
