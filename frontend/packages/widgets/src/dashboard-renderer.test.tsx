// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { SlotBinding, WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { gridItemStyle, resolveWidgets, sizingStyle, widgetSubjectLabel } from './dashboard-renderer';

// A minimal widget carrying only the datasource the subject resolver reads.
function widgetWith(datasource: WidgetInstance['datasource']): WidgetInstance {
  return { id: 'w', type: 'table', layout: { base: { col: 0, colSpan: 1, row: 0, rowSpan: 1, z: 0 } }, datasource };
}

// gridItemStyle owns the genuinely-new mapping: 0-based span box → CSS grid lines,
// plus the clamp that keeps a widget inside the fluid column count (an overflow would
// land in implicit `auto` tracks and break the fill). The renderer's own React tree
// is exercised live on kind; this pins the pure math.
describe('gridItemStyle', () => {
  it('maps a 0-based span box to 1-based CSS grid lines', () => {
    const style = gridItemStyle({ col: 2, colSpan: 3, row: 4, rowSpan: 5, z: 1 }, 24);
    expect(style.gridColumn).toBe('3 / span 3'); // col 2 → line 3
    expect(style.gridRow).toBe('5 / span 5'); // row 4 → line 5
    expect(style.zIndex).toBe(1);
    expect(style.transform).toBeUndefined();
    expect(style.minWidth).toBe(0);
    expect(style.minHeight).toBe(0);
  });

  it('clamps a box that overruns the column count into the grid', () => {
    // col 16 span 12 on a 12-col grid → col 11 (line 12), span 1 (12 - 11).
    const style = gridItemStyle({ col: 16, colSpan: 12, row: 0, rowSpan: 2, z: 0 }, 12);
    expect(style.gridColumn).toBe('12 / span 1');
  });

  it('clamps a partial overrun to the remaining columns', () => {
    // col 20 span 8 on 24 cols → col 20 (line 21), span 4 (24 - 20).
    const style = gridItemStyle({ col: 20, colSpan: 8, row: 0, rowSpan: 1, z: 0 }, 24);
    expect(style.gridColumn).toBe('21 / span 4');
  });

  it('emits a translate transform only when an offset is present', () => {
    const style = gridItemStyle({ col: 0, colSpan: 1, row: 0, rowSpan: 1, z: 0, offset: { x: 5, y: -3 } }, 24);
    expect(style.transform).toBe('translate(5px, -3px)');
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
