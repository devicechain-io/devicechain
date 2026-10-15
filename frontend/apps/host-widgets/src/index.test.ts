// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { UNKNOWN_WIDGET_TYPE, WIDGET_TYPES, parseDashboardDefinition } from '@devicechain/dashboards';
import { WidgetRegistryError, type ErasedWidgetDefinition } from '@devicechain/widget-sdk';
import { describe, expect, it } from 'vitest';

import { ACME_TANK_ID, ACME_TANK_WIDGET_JSON, acmeTankWidget } from '../fixtures/acme-tank';
import { createHostWidgetRegistry, hostWidgetCatalog, hostWidgetRegistry } from './index';

const board = (widgets: unknown[]) => ({ title: 'T', slots: { tank: { type: 'device' } }, widgets });
const box = { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } };

describe('the host composition', () => {
  it('holds every built-in and is one shared, sealed registry', () => {
    const registry = hostWidgetRegistry();
    expect(registry.list().map((d) => d.descriptor.id)).toEqual([...WIDGET_TYPES]);
    expect(hostWidgetRegistry()).toBe(registry);
    expect(hostWidgetCatalog()).toBe(registry.catalog);
  });

  it('composes an operator-installed package beside the built-ins', () => {
    const registry = createHostWidgetRegistry([acmeTankWidget]);
    expect(registry.descriptor(ACME_TANK_ID)?.display.title).toBe('Tank level');
    expect(registry.implementation(ACME_TANK_ID)?.kind).toBe('trusted-react');
    // composing one never touches the shared deployment registry
    expect(hostWidgetRegistry().descriptor(ACME_TANK_ID)).toBeUndefined();
  });

  it('refuses a broken composition at start, not one widget at a time', () => {
    expect(() => createHostWidgetRegistry([acmeTankWidget, acmeTankWidget])).toThrow(WidgetRegistryError);
    // a package claiming a built-in's reserved id
    const impostor = { ...acmeTankWidget, descriptor: { ...acmeTankWidget.descriptor, id: 'gauge' } } as unknown as ErasedWidgetDefinition;
    expect(() => createHostWidgetRegistry([impostor])).toThrow(WidgetRegistryError);
  });
});

describe('what the catalog changes about parsing', () => {
  const tank = { ...ACME_TANK_WIDGET_JSON, layout: box };

  it('an installed extension parses as itself with the catalog, and as a placeholder without it', () => {
    const registry = createHostWidgetRegistry([acmeTankWidget]);
    const withCatalog = parseDashboardDefinition(board([tank]), { catalog: registry.catalog });
    expect(withCatalog.widgets[0].type).toBe(ACME_TANK_ID);

    const without = parseDashboardDefinition(board([tank]));
    expect(without.widgets[0].type).toBe(UNKNOWN_WIDGET_TYPE);
  });

  it("a built-in saved against another settings version is a placeholder, which only a catalog can know", () => {
    const stale = { id: 'g', type: 'gauge', widget: { version: '1.0.0', optionsVersion: 9 }, layout: { base: { ...box.base, row: 4 } }, options: {} };
    const catalog = hostWidgetCatalog();

    const parsed = parseDashboardDefinition(board([stale]), { catalog });
    expect(parsed.widgets[0].type).toBe(UNKNOWN_WIDGET_TYPE);
    expect(parsed.widgets[0].reason?.code).toBe('options-version');

    // Parsed with no catalog the same widget passes as a gauge: the version is never checked.
    expect(parseDashboardDefinition(board([stale])).widgets[0].type).toBe('gauge');
  });

  it('a legacy built-in with no version metadata, and one at the installed version, still resolve', () => {
    const catalog = hostWidgetCatalog();
    const legacy = { id: 'a', type: 'gauge', layout: box, options: {} };
    const current = { id: 'b', type: 'gauge', widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: { ...box.base, row: 4 } }, options: {} };
    const parsed = parseDashboardDefinition(board([legacy, current]), { catalog });
    expect(parsed.widgets.map((w) => w.type)).toEqual(['gauge', 'gauge']);
  });
});
