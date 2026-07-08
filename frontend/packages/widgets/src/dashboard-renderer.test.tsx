// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { SlotBinding, SlotDefinition, WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { gridItemStyle, sizingStyle, widgetSubjectLabel } from './dashboard-renderer';

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

// widgetSubjectLabel resolves the entity a widget shows to the frame subtitle, from a
// device/anchor selector directly or a slot selector through the effective bindings (with
// the slot default as fallback) — the "which asset?" answer, updated once selection
// re-points a slot.
describe('widgetSubjectLabel', () => {
  const anchorBinding: SlotBinding = {
    kind: 'anchor',
    anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'building-1' },
  };

  it('names a device selector by its token', () => {
    expect(widgetSubjectLabel(widgetWith({ kind: 'device', deviceToken: 'bp-therm-001', measurements: [] }), undefined, undefined)).toBe(
      'bp-therm-001',
    );
  });

  it('names an anchor selector by its target token', () => {
    const w = widgetWith({ kind: 'anchor', anchor: anchorBinding.anchor, measurements: [] });
    expect(widgetSubjectLabel(w, undefined, undefined)).toBe('building-1');
  });

  it('resolves a slot through the effective bindings (overlay wins over the slot default)', () => {
    const slots: Record<string, SlotDefinition> = {
      s1: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'default-dev' } },
    };
    const bindings: Record<string, SlotBinding> = { s1: { kind: 'device', deviceToken: 'selected-dev' } };
    const w = widgetWith({ kind: 'slot', slot: 's1', measurements: [] });
    expect(widgetSubjectLabel(w, slots, bindings)).toBe('selected-dev');
  });

  it('falls back to a slot default binding when no overlay is present', () => {
    const slots: Record<string, SlotDefinition> = {
      s1: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'default-dev' } },
    };
    const w = widgetWith({ kind: 'slot', slot: 's1', measurements: [] });
    expect(widgetSubjectLabel(w, slots, undefined)).toBe('default-dev');
  });

  it('returns undefined for a datasource-free widget, an unbound slot, and a reserved kind', () => {
    expect(widgetSubjectLabel(widgetWith(undefined), undefined, undefined)).toBeUndefined();
    expect(widgetSubjectLabel(widgetWith({ kind: 'slot', slot: 'missing', measurements: [] }), {}, {})).toBeUndefined();
    expect(
      widgetSubjectLabel(widgetWith({ kind: 'devices', deviceTokens: ['a'], measurements: [] }), undefined, undefined),
    ).toBeUndefined();
  });

  it('is prototype-safe: a slot named __proto__ resolves as absent, not a prototype object', () => {
    const w = widgetWith({ kind: 'slot', slot: '__proto__', measurements: [] });
    expect(widgetSubjectLabel(w, {}, {})).toBeUndefined();
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
