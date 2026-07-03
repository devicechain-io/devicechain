// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  activeBreakpoint,
  BASE_BREAKPOINT,
  DashboardDefinitionError,
  generateWidgetId,
  isDirty,
  parseDashboardDefinition,
  resolveWidgetBox,
  serializeDefinition,
} from './definition';

const box = (over = {}) => ({ x: 0, y: 0, w: 4, h: 3, z: 0, ...over });

describe('parseDashboardDefinition', () => {
  it('fills defaults for a minimal definition', () => {
    const def = parseDashboardDefinition({ widgets: [] });
    expect(def.schemaVersion).toBe(1);
    expect(def.title).toBe('');
    expect(def.canvas.grid).toEqual({ snap: true, size: 8 });
    expect(def.canvas.breakpoints[BASE_BREAKPOINT]).toBe(0); // base always present
    expect(def.widgets).toEqual([]);
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

  it('normalizes a declared slots section and a slot datasource', () => {
    const def = parseDashboardDefinition({
      widgets: [
        {
          type: 'gauge',
          layout: { base: box() },
          datasource: { kind: 'slot', slot: 'primary', measurements: ['temperature'] },
        },
      ],
      slots: { primary: { type: 'device', label: 'Primary thermostat' }, junk: 'nope' },
    });
    expect(def.slots).toEqual({ primary: { type: 'device', label: 'Primary thermostat' } });
    expect(def.widgets[0].datasource).toEqual({
      kind: 'slot',
      slot: 'primary',
      measurements: ['temperature'],
    });
  });
});

describe('resolveWidgetBox', () => {
  it('returns the breakpoint box when present, else falls back to base', () => {
    const layout = { base: box({ w: 4 }), tablet: box({ w: 8 }) };
    expect(resolveWidgetBox(layout, 'tablet').w).toBe(8);
    expect(resolveWidgetBox(layout, 'mobile').w).toBe(4); // no 'mobile' → base
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
