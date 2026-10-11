// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Catalog-aware parsing: how a stored widget becomes a resolved instance or a placeholder,
// and that a placeholder keeps its stored object byte-for-byte. The catalog here is built
// from descriptors only — the parser never sees a component.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition, storedDefinition } from './definition';
import { isBuiltinWidget, parseWidgetRef, unresolvedWidgets, widgetVersionNotice } from './widget-compat';
import type { ExtensionWidgetInstance } from './types';
import { createWidgetCatalog } from './widget-catalog';
import { defineWidgetDescriptor } from './widget-descriptor';

const TANK = defineWidgetDescriptor({
  id: 'acme/tank-level',
  version: '1.2.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Tank level' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device', 'slot'] },
  binding: {},
  options: { title: { kind: 'string', doc: 'Heading.' }, unit: { kind: 'string', doc: 'Unit.' } },
  defaults: { title: 'Tank' },
  interactions: {},
});

const catalog = createWidgetCatalog([TANK]);

const box = { col: 0, colSpan: 8, row: 0, rowSpan: 4, z: 0 };

const tank = (over: Record<string, unknown> = {}) => ({
  id: 'tank-1',
  type: 'acme/tank-level',
  widget: { version: '1.2.0', optionsVersion: 1 },
  layout: { base: box },
  datasource: { kind: 'slot', slot: 'tank', measurements: ['level'] },
  options: { title: 'Tank level', unit: '%' },
  ...over,
});

const board = (widgets: unknown[]) => ({ widgets, slots: { tank: { type: 'device' } } });

describe('a registered extension widget', () => {
  it('parses to an extension instance carrying its version reference', () => {
    const def = parseDashboardDefinition(board([tank()]), { catalog });
    const w = def.widgets[0];
    expect(w.type).toBe('acme/tank-level');
    expect(w.widget).toEqual({ version: '1.2.0', optionsVersion: 1 });
    expect(w.datasource).toEqual({ kind: 'slot', slot: 'tank', measurements: ['level'] });
    expect(w.options).toEqual({ title: 'Tank level', unit: '%' });
    expect(unresolvedWidgets(def)).toEqual([]);
  });

  it('round-trips through serialize and parse unchanged', () => {
    const once = serializeDefinition(parseDashboardDefinition(board([tank()]), { catalog }));
    const twice = serializeDefinition(parseDashboardDefinition(JSON.parse(once), { catalog }));
    expect(twice).toBe(once);
    expect(JSON.parse(once).widgets[0].widget).toEqual({ version: '1.2.0', optionsVersion: 1 });
  });

  // Fields outside the owned envelope belong to the package or a newer format; a resolved
  // widget keeps them, in place, so load → save writes the widget back byte-for-byte.
  it('keeps unknown top-level, metadata and option fields byte-for-byte', () => {
    const stored = {
      vendorTop: { a: [1, { b: 2 }] },
      ...tank({
        widget: { version: '1.2.0', optionsVersion: 1, publisher: 'acme' },
        options: { title: 'Tank level', unit: '%', vendorOption: { deep: [true] } },
      }),
      trailing: 'x',
    };
    const def = parseDashboardDefinition(board([stored]), { catalog });
    expect(def.widgets[0].type).toBe('acme/tank-level');
    expect(JSON.stringify(JSON.parse(serializeDefinition(def)).widgets[0])).toBe(JSON.stringify(stored));
    // ...and through a second parse of the saved document.
    const again = parseDashboardDefinition(JSON.parse(serializeDefinition(def)), { catalog });
    expect(serializeDefinition(again)).toBe(serializeDefinition(def));
    // An editor's spread-update of an owned field leaves them in place.
    const edited = { ...def, widgets: [{ ...def.widgets[0], options: { title: 'Renamed' } } as ExtensionWidgetInstance] };
    const written = JSON.parse(serializeDefinition(edited)).widgets[0];
    expect(written.vendorTop).toEqual(stored.vendorTop);
    expect(written.trailing).toBe('x');
    expect(written.options).toEqual({ title: 'Renamed' });
  });

  it('keeps a stored __proto__ field as a field, not a prototype', () => {
    const text = JSON.stringify(board([tank()])).replace('"id":"tank-1"', '"id":"tank-1","__proto__":{"polluted":true}');
    const def = parseDashboardDefinition(JSON.parse(text), { catalog });
    expect((def.widgets[0] as unknown as { polluted?: boolean }).polluted).toBeUndefined();
    expect(serializeDefinition(def)).toContain('"__proto__":{"polluted":true}');
  });

  it('a top-level field the format reserves keeps the widget verbatim as a placeholder', () => {
    const stored = tank({ raw: { x: 1 } });
    const def = parseDashboardDefinition(board([stored]), { catalog });
    expect(def.widgets[0].reason?.code).toBe('invalid-metadata');
    expect(JSON.stringify(JSON.parse(serializeDefinition(def)).widgets[0])).toBe(JSON.stringify(stored));
  });

  // F1: the version is provenance. A different release renders; the editor is told.
  it('renders when only the release differs, and says so', () => {
    const def = parseDashboardDefinition(board([tank({ widget: { version: '1.1.0', optionsVersion: 1 } })]), { catalog });
    expect(def.widgets[0].type).toBe('acme/tank-level');
    expect(unresolvedWidgets(def)).toEqual([]);
    expect(widgetVersionNotice('tank-1', def.widgets[0].widget, TANK)).toMatch(/authored with version 1\.1\.0.*1\.2\.0/);
    expect(widgetVersionNotice('tank-1', { version: '1.2.0', optionsVersion: 1 }, TANK)).toBeUndefined();
  });
});

describe('a widget the catalog cannot resolve', () => {
  const cases: Array<[string, Record<string, unknown>, string, ReturnType<typeof createWidgetCatalog> | undefined]> = [
    ['not installed', tank({ type: 'acme/missing' }), 'unregistered', catalog],
    ['no catalog supplied', tank(), 'unregistered', undefined],
    ['settings version differs', tank({ widget: { version: '1.2.0', optionsVersion: 2 } }), 'options-version', catalog],
    ['SDK generation differs', tank({ widget: { version: '1.2.0', optionsVersion: 1, sdkApiVersion: 2 } }), 'sdk-incompatible', catalog],
    ['no version metadata', (() => { const w = tank(); delete (w as Record<string, unknown>).widget; return w; })(), 'unversioned', catalog],
    ['malformed metadata', tank({ widget: { version: 'latest', optionsVersion: 1 } }), 'invalid-metadata', catalog],
    ['non-object metadata', tank({ widget: '1.2.0' }), 'invalid-metadata', catalog],
  ];

  it.each(cases)('%s: a placeholder with a specific reason and the stored JSON intact', (_name, stored, code, cat) => {
    // Unknown fields and version metadata are exactly what must survive.
    const widget = { ...stored, futureField: { nested: [1, 2, { three: 3 }] } };
    const def = parseDashboardDefinition(board([widget]), { catalog: cat });
    const w = def.widgets[0];
    expect(w.type).toBe('unknown-widget');
    expect(w.reason?.code).toBe(code);
    expect(w.reason?.message.length).toBeGreaterThan(10);
    expect(w.datasource).toBeUndefined();
    expect(w.options).toBeUndefined();

    // Byte-equivalent: the widget as written back is the widget as read.
    const written = JSON.parse(serializeDefinition(def)).widgets[0];
    expect(JSON.stringify(written)).toBe(JSON.stringify(widget));
    // ...including after the editor MOVES it (only the moved box changes, everything else
    // is still the stored bytes).
    const moved = { ...def, widgets: [{ ...w, layout: { base: { ...box, col: 3 } } }] };
    const movedWritten = JSON.parse(JSON.stringify(storedDefinition(moved))).widgets[0];
    expect(movedWritten).toEqual({ ...widget, layout: { base: { ...box, col: 3 } } });
  });

  it('is reported by unresolvedWidgets so a publish gate can refuse it', () => {
    const def = parseDashboardDefinition(
      board([tank({ id: 'a', widget: { version: '1.2.0', optionsVersion: 9 } }), tank({ id: 'b' })]),
      { catalog },
    );
    expect(unresolvedWidgets(def).map((r) => [r.widgetId, r.reason.code])).toEqual([['a', 'options-version']]);
  });

  it('is recomputed against the catalog when a typed definition is parsed again', () => {
    const first = parseDashboardDefinition(board([tank({ type: 'acme/missing' })]), { catalog });
    const again = parseDashboardDefinition(JSON.parse(JSON.stringify(first)), { catalog });
    expect(again.widgets[0].type).toBe('unknown-widget');
    expect(again.widgets[0].reason?.code).toBe('unregistered');
    expect(JSON.stringify(JSON.parse(serializeDefinition(again)).widgets[0])).toBe(JSON.stringify(tank({ type: 'acme/missing' })));
  });
});

describe('legacy built-ins', () => {
  const gauge = { id: 'g', type: 'gauge', layout: { base: box }, options: { title: 'G' } };

  it('parse with no catalog and no metadata exactly as before', () => {
    const def = parseDashboardDefinition(board([gauge]));
    expect(def.widgets[0]).toEqual(gauge);
    expect(isBuiltinWidget(def.widgets[0])).toBe(true);
    expect(serializeDefinition(def)).toBe(JSON.stringify({ schemaVersion: 1, title: '', canvas: def.canvas, widgets: [gauge], slots: { tank: { type: 'device' } } }));
  });

  // Deliberately unchanged for built-ins: only the owned envelope survives, as it always has.
  it('drop unknown top-level fields exactly as before', () => {
    const def = parseDashboardDefinition(board([{ ...gauge, stale: 1 }]));
    expect(def.widgets[0]).toEqual(gauge);
  });

  it('keep an authored version reference without a catalog', () => {
    const ref = { version: '1.0.0', optionsVersion: 1 };
    const def = parseDashboardDefinition(board([{ ...gauge, widget: ref }]));
    expect(def.widgets[0].widget).toEqual(ref);
  });

  it('are placeholders when the supplied catalog does not hold them', () => {
    const def = parseDashboardDefinition(board([gauge]), { catalog });
    expect(def.widgets[0].reason?.code).toBe('unregistered');
  });
});

describe('parseWidgetRef', () => {
  it('distinguishes absent from malformed', () => {
    expect(parseWidgetRef(undefined)).toBeUndefined();
    expect(parseWidgetRef({ version: '1.0.0', optionsVersion: 0 })).toBeNull();
    expect(parseWidgetRef({ version: '1.0.0', optionsVersion: 1.5 })).toBeNull();
    expect(parseWidgetRef(null)).toBeNull();
    expect(parseWidgetRef({ version: '1.0.0', optionsVersion: 1 })).toEqual({ version: '1.0.0', optionsVersion: 1 });
  });
});
