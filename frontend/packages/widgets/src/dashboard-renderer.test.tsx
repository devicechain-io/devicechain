// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { SlotBinding, WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { gridCss, gridItemStyle, relativeBoxStyle, resolveWidgets, sizingStyle, widgetSubjectLabel } from './dashboard-renderer';

// A minimal widget carrying only the datasource the subject resolver reads.
function widgetWith(datasource: WidgetInstance['datasource']): WidgetInstance {
  return { id: 'w', type: 'table', layout: { base: { mode: 'grid', col: 0, row: 0, cols: 1, rows: 1 } }, datasource };
}

// gridItemStyle owns the mapping from a 0-based footprint to CSS grid lines, plus the clamp
// that keeps a widget inside the column count (an overflow would land in implicit `auto` tracks
// and break the fill). The renderer's own React tree is exercised live; this pins the pure math.
describe('gridItemStyle', () => {
  it('maps a 0-based footprint to 1-based CSS grid lines, with no layering of its own', () => {
    const style = gridItemStyle({ mode: 'grid', col: 2, row: 4, cols: 3, rows: 5 }, 12);
    expect(style.gridColumn).toBe('3 / span 3'); // col 2 → line 3
    expect(style.gridRow).toBe('5 / span 5'); // row 4 → line 5
    expect(style.zIndex).toBeUndefined();
    expect(style.transform).toBeUndefined();
    expect(style.minWidth).toBe(0);
    expect(style.minHeight).toBe(0);
  });

  it('clamps a footprint that overruns the column count into the grid', () => {
    // col 16 span 12 on a 12-col grid → col 11 (line 12), span 1 (12 - 11).
    expect(gridItemStyle({ mode: 'grid', col: 16, row: 0, cols: 12, rows: 2 }, 12).gridColumn).toBe('12 / span 1');
  });

  it('clamps a partial overrun to the remaining columns', () => {
    // col 10 span 4 on 12 cols → col 10 (line 11), span 2.
    expect(gridItemStyle({ mode: 'grid', col: 10, row: 0, cols: 4, rows: 1 }, 12).gridColumn).toBe('11 / span 2');
  });
});

describe('gridCss', () => {
  it('scales px lengths by the density and reads spacing tokens from the board', () => {
    const css = gridCss({ columns: 12, minCellSize: 56, gutter: { x: 8, y: 'space-2' }, padding: 'space-0' }, 12);
    expect(css.gutterX).toBe('calc(8px * var(--dc-scale, 1))');
    expect(css.gutterY).toBe('var(--dc-space-2)');
    expect(css.padding).toBe('0px');
  });

  it('makes a cell the grid width less its gutters, shared by the live column count (square rows use it)', () => {
    expect(gridCss({ columns: 12, minCellSize: 56, gutter: 0, padding: 0 }, 4).cell).toBe(
      'calc((100cqw - 3 * calc(0px * var(--dc-scale, 1))) / 4)',
    );
  });
});

describe('relativeBoxStyle', () => {
  const lengths = { gutterX: 'GX', gutterY: 'GY' };

  it('places a corner badge at the host anchor, pulled back by its own point, offset in cell pitches', () => {
    const style = relativeBoxStyle(
      { mode: 'relative', host: 'map', anchor: 'top-right', offset: { x: -0.25, y: 0.25 }, size: { cols: 1, rows: 2 }, layer: 2 },
      lengths,
    );
    expect(style.left).toBe('calc(100% + -0.25 * (var(--dc-cell) + GX))');
    expect(style.top).toBe('calc(0% + 0.25 * (var(--dc-cell) + GY))');
    expect(style.transform).toBe('translate(-100%, 0%)'); // self defaults to the anchor
    expect(style.width).toBe('calc(1 * var(--dc-cell) + 0 * GX)');
    expect(style.height).toBe('calc(2 * var(--dc-cell) + 1 * GY)');
    expect(style.zIndex).toBe(102); // above every grid widget, in layer order
  });

  it('fills its host when told to', () => {
    const style = relativeBoxStyle({ mode: 'relative', host: 'h', anchor: 'center', size: { fill: 'host' } }, lengths);
    expect([style.width, style.height, style.transform]).toEqual(['100%', '100%', 'translate(-50%, -50%)']);
  });
});

// widgetSubjectLabel names the entity a widget shows, read from the CONCRETE selector the
// renderer resolved (resolveWidgets) — the same selector the widget subscribes with, so the
// subtitle cannot name a different entity than the one on screen.
describe('widgetSubjectLabel', () => {
  const anchorBinding: SlotBinding = {
    kind: 'anchor',
    anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'building-1' },
  };

  it('names a device selector by its token', () => {
    expect(widgetSubjectLabel(widgetWith({ kind: 'device', deviceToken: 'bp-therm-001', measurements: [] }))).toBe(
      'bp-therm-001',
    );
  });

  it('names an anchor selector by its target token', () => {
    const w = widgetWith({ kind: 'anchor', anchor: anchorBinding.anchor, measurements: [] });
    expect(widgetSubjectLabel(w)).toBe('building-1');
  });

  it('names a slot by the entity the settled bindings resolve it to', () => {
    const bindings: Record<string, SlotBinding> = { s1: { kind: 'device', deviceToken: 'selected-dev' } };
    const [w] = resolveWidgets([widgetWith({ kind: 'slot', slot: 's1', measurements: [] })], bindings);
    expect(widgetSubjectLabel(w)).toBe('selected-dev');
    const [a] = resolveWidgets([widgetWith({ kind: 'slot', slot: 's1', measurements: [] })], { s1: anchorBinding });
    expect(widgetSubjectLabel(a)).toBe('building-1');
  });

  it('names nothing for no datasource, an unbound slot, an unresolved slot, a reserved kind, an empty token', () => {
    expect(widgetSubjectLabel(widgetWith(undefined))).toBeUndefined();
    expect(widgetSubjectLabel(widgetWith({ kind: 'unbound', measurements: [] }))).toBeUndefined();
    expect(widgetSubjectLabel(widgetWith({ kind: 'slot', slot: 's1', measurements: [] }))).toBeUndefined();
    expect(widgetSubjectLabel(widgetWith({ kind: 'devices', deviceTokens: ['a'], measurements: [] }))).toBeUndefined();
    expect(widgetSubjectLabel(widgetWith({ kind: 'device', deviceToken: '', measurements: [] }))).toBeUndefined();
  });
});

// resolveWidgets is the renderer's one hand-off from a definition to a data source.
describe('resolveWidgets', () => {
  it('resolves a slot widget and leaves every other widget object untouched', () => {
    const device = widgetWith({ kind: 'device', deviceToken: 'x', measurements: [] });
    const none = widgetWith(undefined);
    const slot = widgetWith({ kind: 'slot', slot: 's1', measurements: ['t'] });
    const [d, n, s] = resolveWidgets([device, none, slot], { s1: { kind: 'device', deviceToken: 'dev' } });
    expect(d).toBe(device);
    expect(n).toBe(none);
    expect(n.datasource).toBeUndefined();
    expect(s.datasource).toEqual({ kind: 'device', deviceToken: 'dev', measurements: ['t'] });
  });

  it('turns an unbound slot into an explicit unbound selector, never an absent datasource', () => {
    const [s] = resolveWidgets([widgetWith({ kind: 'slot', slot: 'missing', measurements: [] })], undefined);
    expect(s.datasource).toEqual({ kind: 'unbound', measurements: [] });
  });
});

describe('sizingStyle', () => {
  const bg = { color: '#111', imageUrl: 'x.png' };

  it('fills width and height for fill sizing', () => {
    expect(sizingStyle('fill', undefined)).toMatchObject({ width: '100%', height: '100%', overflow: 'auto' });
  });

  it('caps the width (grid adjusts within) for fixed-width sizing', () => {
    expect(sizingStyle({ width: 1200 }, undefined)).toMatchObject({ width: 1200, maxWidth: '100%', height: '100%' });
  });

  it('pins the height (rows scroll) for fixed-height sizing', () => {
    expect(sizingStyle({ height: 800 }, undefined)).toMatchObject({ width: '100%', height: 800 });
  });

  it('paints the background (color AND image) on the sizing wrapper', () => {
    const style = sizingStyle('fill', bg);
    expect(style.backgroundColor).toBe('#111');
    expect(style.backgroundImage).toBe('url(x.png)');
    expect(style.backgroundSize).toBe('cover');
  });
});
