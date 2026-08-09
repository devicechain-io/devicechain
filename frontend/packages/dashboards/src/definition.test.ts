// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  activeBreakpoint,
  BASE_BREAKPOINT,
  canScopeSlot,
  DashboardDefinitionError,
  generateWidgetId,
  isDirty,
  parseDashboardDefinition,
  resolveWidgetBox,
  serializeDefinition,
} from './definition';
import type { SlotDefinition } from './types';

const box = (over = {}) => ({ col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0, ...over });

describe('parseDashboardDefinition', () => {
  it('fills defaults for a minimal definition', () => {
    const def = parseDashboardDefinition({ widgets: [] });
    expect(def.schemaVersion).toBe(1);
    expect(def.title).toBe('');
    expect(def.canvas.grid).toEqual({ columns: 24, gap: 8, rowHeight: 40 });
    expect(def.canvas.sizing).toBe('fill');
    expect(def.canvas.breakpoints[BASE_BREAKPOINT]).toBe(0); // base always present
    expect(def.widgets).toEqual([]);
  });

  it('coerces the grid (clamps columns/rowHeight, accepts a {row,col} gap) and sizing', () => {
    const def = parseDashboardDefinition({
      widgets: [],
      canvas: {
        grid: { columns: 0, gap: { row: 4, col: 12 }, rowHeight: -5 },
        sizing: { width: 1200 },
      },
    });
    expect(def.canvas.grid).toEqual({ columns: 1, gap: { row: 4, col: 12 }, rowHeight: 1 });
    expect(def.canvas.sizing).toEqual({ width: 1200 });
  });

  it('parses a fixed-height sizing and an offset on a box', () => {
    const def = parseDashboardDefinition({
      widgets: [
        { type: 'label', layout: { base: { ...box(), offset: { x: 5, y: -3 } } } },
      ],
      canvas: { sizing: { height: 800 } },
    });
    expect(def.canvas.sizing).toEqual({ height: 800 });
    expect(def.widgets[0].layout.base.offset).toEqual({ x: 5, y: -3 });
  });

  it('defaults a partial {row,col} gap’s missing axis to the default gutter, not 0', () => {
    const def = parseDashboardDefinition({ widgets: [], canvas: { grid: { gap: { row: 12 } } } });
    expect(def.canvas.grid.gap).toEqual({ row: 12, col: 8 });
  });

  it('rounds a fractional z (invalid CSS zIndex silently drops to auto otherwise)', () => {
    const def = parseDashboardDefinition({
      widgets: [{ type: 'label', layout: { base: { ...box(), z: 1.6 } } }],
    });
    expect(def.widgets[0].layout.base.z).toBe(2);
  });

  it('keeps a valid widget and its datasource/options', () => {
    const def = parseDashboardDefinition({
      schemaVersion: 1,
      title: 'Fleet',
      widgets: [
        {
          id: 'w1',
          type: 'gauge',
          layout: { base: box() },
          datasource: { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] },
          options: { unit: '°C' },
        },
      ],
    });
    expect(def.widgets[0]).toMatchObject({
      id: 'w1',
      type: 'gauge',
      datasource: { kind: 'device', deviceToken: 'therm-001' },
      options: { unit: '°C' },
    });
  });

  it('normalizes a device datasource missing measurements to an empty array', () => {
    const def = parseDashboardDefinition({
      widgets: [
        {
          type: 'gauge',
          layout: { base: box() },
          datasource: { kind: 'device', deviceToken: 'therm-001' },
        },
      ],
    });
    expect(def.widgets[0].datasource).toEqual({
      kind: 'device',
      deviceToken: 'therm-001',
      measurements: [],
    });
  });

  it('generates an id when a widget omits one', () => {
    const def = parseDashboardDefinition({
      widgets: [{ type: 'label', layout: { base: box() } }],
    });
    expect(def.widgets[0].id).toMatch(/^w-/);
  });

  it('rejects a non-object', () => {
    expect(() => parseDashboardDefinition(42)).toThrow(DashboardDefinitionError);
    expect(() => parseDashboardDefinition(null)).toThrow(DashboardDefinitionError);
    expect(() => parseDashboardDefinition([])).toThrow(DashboardDefinitionError);
  });

  it('rejects an unknown widget type', () => {
    expect(() =>
      parseDashboardDefinition({ widgets: [{ type: 'wormhole', layout: { base: box() } }] }),
    ).toThrow(/unknown type/);
  });

  it('rejects a widget layout with no base box', () => {
    expect(() =>
      parseDashboardDefinition({ widgets: [{ type: 'label', layout: { tablet: box() } }] }),
    ).toThrow(/no 'base' box/);
  });

  // Slot headroom (PR F): reserved runtime-binding shape parses + round-trips, but
  // nothing authors it yet, so a slot-free definition must not gain a `slots` key.
  it('omits slots entirely when the definition declares none', () => {
    const def = parseDashboardDefinition({ widgets: [] });
    expect('slots' in def).toBe(false);
  });

  it('normalizes a declared slots section (with default binding) and a slot datasource', () => {
    const def = parseDashboardDefinition({
      widgets: [
        {
          type: 'gauge',
          layout: { base: box() },
          datasource: { kind: 'slot', slot: 'primary', measurements: ['temperature'] },
        },
      ],
      slots: {
        primary: {
          type: 'device',
          label: 'Primary thermostat',
          defaultBinding: { kind: 'device', deviceToken: 'therm-001' },
        },
        area: {
          type: 'anchor',
          defaultBinding: {
            kind: 'anchor',
            anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-1' },
          },
        },
        junk: 'nope',
      },
    });
    expect(def.slots).toEqual({
      primary: {
        type: 'device',
        label: 'Primary thermostat',
        defaultBinding: { kind: 'device', deviceToken: 'therm-001' },
      },
      area: {
        type: 'anchor',
        defaultBinding: {
          kind: 'anchor',
          anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-1' },
        },
      },
    });
    // Round-trips through serialize/parse unchanged.
    expect(parseDashboardDefinition(JSON.parse(JSON.stringify(def)))).toEqual(def);
    expect(def.widgets[0].datasource).toEqual({
      kind: 'slot',
      slot: 'primary',
      measurements: ['temperature'],
    });
  });

  // Scoped slots (ADR-039 selection amendment): a well-formed scope is carried; a scope
  // with a missing / non-anchor / self / cyclic parent is dropped (degrade, don't throw)
  // so a scope-blind whitelist can't silently erase the hierarchy on load.
  describe('slot scope', () => {
    const withSlots = (slots: Record<string, unknown>) =>
      parseDashboardDefinition({ widgets: [], slots }).slots ?? {};

    it('carries a valid scope onto an anchor parent (defaulting strategy to first)', () => {
      const slots = withSlots({
        building: { type: 'anchor' },
        therm: { type: 'device', scope: { parent: 'building' } },
      });
      expect(slots.therm.scope).toEqual({ parent: 'building', strategy: 'first' });
    });

    it('keeps an explicit manual strategy', () => {
      const slots = withSlots({
        building: { type: 'anchor' },
        therm: { type: 'device', scope: { parent: 'building', strategy: 'manual' } },
      });
      expect(slots.therm.scope).toEqual({ parent: 'building', strategy: 'manual' });
    });

    it('drops a scope whose parent is missing', () => {
      const slots = withSlots({ therm: { type: 'device', scope: { parent: 'nope' } } });
      expect(slots.therm.scope).toBeUndefined();
    });

    it('drops a scope whose parent is a device (non-anchor) slot', () => {
      const slots = withSlots({
        gateway: { type: 'device' },
        therm: { type: 'device', scope: { parent: 'gateway' } },
      });
      expect(slots.therm.scope).toBeUndefined();
    });

    it('drops a self-referential scope', () => {
      const slots = withSlots({ a: { type: 'anchor', scope: { parent: 'a' } } });
      expect(slots.a.scope).toBeUndefined();
    });

    it('drops scopes forming a cycle', () => {
      const slots = withSlots({
        a: { type: 'anchor', scope: { parent: 'b' } },
        b: { type: 'anchor', scope: { parent: 'a' } },
      });
      expect(slots.a.scope).toBeUndefined();
      expect(slots.b.scope).toBeUndefined();
    });
  });
});

describe('resolveWidgetBox', () => {
  it('returns the breakpoint box when present, else falls back to base', () => {
    const layout = { base: box({ colSpan: 4 }), tablet: box({ colSpan: 8 }) };
    expect(resolveWidgetBox(layout, 'tablet').colSpan).toBe(8);
    expect(resolveWidgetBox(layout, 'mobile').colSpan).toBe(4); // no 'mobile' → base
  });
});

describe('activeBreakpoint', () => {
  const breakpoints = { base: 0, tablet: 768, desktop: 1200 };

  it('picks the widest breakpoint that fits the viewport', () => {
    expect(activeBreakpoint(breakpoints, 400)).toBe('base');
    expect(activeBreakpoint(breakpoints, 800)).toBe('tablet');
    expect(activeBreakpoint(breakpoints, 1600)).toBe('desktop');
  });
});

describe('generateWidgetId', () => {
  it('produces unique ids', () => {
    expect(generateWidgetId()).not.toBe(generateWidgetId());
  });
});

describe('serializeDefinition / isDirty', () => {
  const def = () =>
    parseDashboardDefinition({
      schemaVersion: 1,
      title: 'T',
      widgets: [{ id: 'w1', type: 'label', layout: { base: box() } }],
    });

  it('round-trips through parse', () => {
    const d = def();
    expect(parseDashboardDefinition(JSON.parse(serializeDefinition(d)))).toEqual(d);
  });

  it('isDirty is false for equal definitions and true after a change', () => {
    expect(isDirty(def(), def())).toBe(false);
    expect(isDirty({ ...def(), title: 'changed' }, def())).toBe(true);
  });
});

describe('canScopeSlot', () => {
  const slots: Record<string, SlotDefinition> = {
    building: { type: 'anchor' },
    gateway: { type: 'device' },
    therm: { type: 'device' },
    floor: { type: 'anchor', scope: { parent: 'building', strategy: 'first' } },
  };

  it('allows a device slot to scope to an anchor parent', () => {
    expect(canScopeSlot(slots, 'therm', 'building')).toBe(true);
  });

  it('rejects a non-anchor parent', () => {
    expect(canScopeSlot(slots, 'therm', 'gateway')).toBe(false);
  });

  it('rejects a missing parent', () => {
    expect(canScopeSlot(slots, 'therm', 'nope')).toBe(false);
  });

  it('rejects a self-referential scope', () => {
    expect(canScopeSlot(slots, 'building', 'building')).toBe(false);
  });

  it('rejects an edge that would create a cycle', () => {
    // floor→building already; making building→floor would loop.
    expect(canScopeSlot(slots, 'building', 'floor')).toBe(false);
  });

  it('agrees with the loader: a valid authored scope survives a parse round-trip', () => {
    // canScopeSlot approves therm→building; the loader (validateScopes) must keep it.
    expect(canScopeSlot(slots, 'therm', 'building')).toBe(true);
    const parsed = parseDashboardDefinition({
      widgets: [],
      slots: {
        building: { type: 'anchor' },
        therm: { type: 'device', scope: { parent: 'building', strategy: 'manual' } },
      },
    });
    expect(parsed.slots!.therm.scope).toEqual({ parent: 'building', strategy: 'manual' });
  });
});

// ---- The location series selector (additive) --------------------------------
//
// 🔴 THIS BLOCK IS THE REGRESSION GUARD FOR THE SELECTOR CHANGE, and the tests that
// matter most are the ones about definitions that carry NO location field at all.
//
// Every dashboard stored before the location channel existed is a measurement-only
// document, and the whole justification for putting the location series in its own
// field — rather than overloading `measurements` — is that none of those documents
// change meaning, change shape, or change bytes. The parse tests below hold that
// directly: a stored definition round-trips byte-identically, so a load/save is not a
// diff, and isDirty stays quiet on a dashboard nobody edited.
describe('parseDashboardDefinition — location series selection', () => {
  const located = (kind: string, extra: Record<string, unknown>) => ({
    widgets: [
      {
        id: 'w1',
        type: 'map',
        layout: { base: box() },
        datasource: { kind, ...extra, location: { series: 'latest' } },
      },
    ],
  });

  it('carries a location series on a device selector', () => {
    const def = parseDashboardDefinition(located('device', { deviceToken: 'dozer-1', measurements: [] }));
    expect(def.widgets[0].datasource).toEqual({
      kind: 'device',
      deviceToken: 'dozer-1',
      measurements: [],
      location: { series: 'latest' },
    });
  });

  it('carries a location series on an anchor selector', () => {
    const def = parseDashboardDefinition(
      located('anchor', { anchor: { relationship: 'monitors', targetType: 'area', targetToken: 'site-1' } }),
    );
    expect(def.widgets[0].datasource).toMatchObject({
      kind: 'anchor',
      location: { series: 'latest' },
    });
  });

  it('carries a location series on a slot selector', () => {
    const def = parseDashboardDefinition(located('slot', { slot: 'fleet' }));
    expect(def.widgets[0].datasource).toEqual({
      kind: 'slot',
      slot: 'fleet',
      measurements: [],
      location: { series: 'latest' },
    });
  });

  it('drops an unrecognized location series rather than carrying it through', () => {
    // The vocabulary is closed. Carrying an unknown series would give the author a
    // widget that looks configured and reads nothing; dropping it makes the widget's
    // empty state honest.
    const def = parseDashboardDefinition(located('device', { deviceToken: 'd1', measurements: [] }));
    expect(def.widgets[0].datasource).toHaveProperty('location');
    const bogus = parseDashboardDefinition({
      widgets: [
        {
          type: 'map',
          layout: { base: box() },
          datasource: { kind: 'device', deviceToken: 'd1', measurements: [], location: { series: 'track' } },
        },
      ],
    });
    expect(bogus.widgets[0].datasource).not.toHaveProperty('location');
  });

  it('drops a malformed location field without failing the whole definition', () => {
    const def = parseDashboardDefinition({
      widgets: [
        {
          type: 'map',
          layout: { base: box() },
          datasource: { kind: 'device', deviceToken: 'd1', measurements: [], location: 'latest' },
        },
      ],
    });
    expect(def.widgets[0].datasource).toEqual({ kind: 'device', deviceToken: 'd1', measurements: [] });
  });

  // ---- The additive guarantee ------------------------------------------------

  it('leaves a pre-existing measurement-only definition byte-identical', () => {
    // A representative board of every selector kind, none of which names a location
    // series — i.e. every dashboard stored before this field existed.
    const stored = {
      schemaVersion: 1,
      title: 'Zone A',
      canvas: {
        grid: { columns: 24, gap: 8, rowHeight: 40 },
        sizing: 'fill',
        breakpoints: { base: 0, lg: 1024 },
      },
      widgets: [
        {
          id: 'w1',
          type: 'gauge',
          layout: { base: box() },
          datasource: { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] },
          options: { unit: '°C' },
        },
        {
          id: 'w2',
          type: 'timeseries-chart',
          layout: { base: box() },
          datasource: {
            kind: 'anchor',
            anchor: { relationship: 'monitors', targetType: 'area', targetToken: 'zone-a' },
            measurements: ['temperature', 'humidity'],
          },
        },
        {
          id: 'w3',
          type: 'latest-card',
          layout: { base: box() },
          datasource: { kind: 'slot', slot: 'primary', measurements: ['temperature'] },
        },
        { id: 'w4', type: 'alarm-table', layout: { base: box() }, options: { state: 'ACTIVE' } },
      ],
      slots: { primary: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'therm-001' } } },
    };

    const parsed = parseDashboardDefinition(stored);

    // No selector gained a location key…
    for (const widget of parsed.widgets) {
      expect(widget.datasource == null || !('location' in widget.datasource)).toBe(true);
    }
    // …and the whole document serializes to exactly what was stored, so a load/save
    // round trip is not a diff and an untouched dashboard is never dirty.
    expect(serializeDefinition(parsed)).toBe(JSON.stringify(stored));
    expect(isDirty(parsed, parseDashboardDefinition(JSON.parse(serializeDefinition(parsed))))).toBe(false);
  });

  it('round-trips a definition that DOES name a location series', () => {
    // The counterweight to the byte-identity test above: leaving old documents alone is
    // only useful while a new one survives the same round trip.
    const stored = located('device', { deviceToken: 'dozer-1', measurements: [] });
    const parsed = parseDashboardDefinition(stored);
    const reparsed = parseDashboardDefinition(JSON.parse(serializeDefinition(parsed)));
    expect(reparsed.widgets[0].datasource).toEqual({
      kind: 'device',
      deviceToken: 'dozer-1',
      measurements: [],
      location: { series: 'latest' },
    });
    expect(isDirty(parsed, reparsed)).toBe(false);
  });
});
