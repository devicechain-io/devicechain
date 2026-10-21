// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// The built-in registry is the ONLY place a built-in's channel, schema, binding policy and
// component are declared (each widget module registers itself through the public
// defineWidget), and it sits on one live path (the publish-gate option validation). The
// by-id views the console still imports (WIDGET_CHANNEL, WIDGET_OPTIONS, ...) are derived
// from it, so this file compares each view to its registration over EVERY widget type, and
// pins the facts the renderer relies on: a changed channel, a swapped component or a drifted
// default is a red test rather than a registry that quietly disagrees with the renderer.

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { WIDGET_TYPES, addWidget, basePlacement, parseDashboardDefinition, spanLimitsOfLayout, type WidgetType } from '@devicechain/dashboards';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { describe, expect, it } from 'vitest';

import { builtinWidgetCatalog, builtinWidgetDescriptors } from './builtin-descriptors';
import { builtinWidgetDefinitions, createBuiltinWidgetRegistry } from './builtin-registry';
import { validateDefinitionOptions } from './definition-options';
import { WIDGET_OPTIONS } from './options';
import { WIDGET_BINDS_DATASOURCE, WIDGET_CHANNEL } from './registry';

const registry = createBuiltinWidgetRegistry();
const empty = parseDashboardDefinition({ schemaVersion: 2, widgets: [] });

describe('the built-in registry', () => {
  it('is constructed successfully, i.e. the built-ins pass their own validation', () => {
    expect(registry.list()).toHaveLength(WIDGET_TYPES.length);
    expect(() => createWidgetRegistry(builtinWidgetDefinitions)).not.toThrow();
  });

  it('lists exactly WIDGET_TYPES, in order', () => {
    expect(registry.list().map((d) => d.descriptor.id)).toEqual([...WIDGET_TYPES]);
    expect(registry.catalog.list().map((d) => d.id)).toEqual([...WIDGET_TYPES]);
    expect(builtinWidgetCatalog.list().map((d) => d.id)).toEqual([...WIDGET_TYPES]);
  });

  describe.each(WIDGET_TYPES)('%s', (type) => {
    const descriptor = registry.descriptor(type)!;

    it('has its by-id views derived from the registration, by identity', () => {
      expect(WIDGET_CHANNEL[type]).toBe(descriptor.channel);
      expect(WIDGET_OPTIONS[type]).toBe(descriptor.options);
    });

    it('binds a datasource exactly when the derived view says it does', () => {
      expect(descriptor.datasource.mode !== 'none').toBe(WIDGET_BINDS_DATASOURCE[type]);
    });

    // The mode is not just "binds or not": an alarm widget with no datasource shows the
    // tenant-wide alarms, so `optional` vs `required` is behaviour the runtime will read.
    it('declares the datasource mode and selectors the renderer honours today', () => {
      const channel = WIDGET_CHANNEL[type];
      if (!WIDGET_BINDS_DATASOURCE[type]) {
        expect(descriptor.datasource).toEqual({ mode: 'none' });
        return;
      }
      expect(descriptor.datasource).toEqual({
        mode: channel === 'alarm' ? 'optional' : 'required',
        // A command target, an aggregate and a reading keyed by measurement name each name ONE device
        // (a group would mix its devices' readings); the alarm and location widgets and the fleet
        // widgets (which read member by member) may be bound to a group.
        selectors:
          channel === 'control' || channel === 'aggregate' || (channel === 'measurement' && !('members' in descriptor.binding))
            ? ['device', 'slot']
            : ['device', 'anchor', 'slot'],
      });
    });

    // Each declared interaction is one the component actually uses (alarm-table acts on and
    // drills from alarms, command-button sends, entity-selector picks from candidates).
    it('declares exactly the interactions its component uses', () => {
      const expected: Partial<Record<WidgetType, object>> = {
        'alarm-table': { alarmActions: true, selection: true },
        'command-button': { commandSend: true },
        'command-slider': { commandSend: true },
        'command-toggle': { commandSend: true },
        'entity-selector': { selection: true, candidates: true },
        // The device column drills like the alarm table's originator.
        'fleet-table': { selection: true },
        map: { selection: true },
      };
      expect(descriptor.interactions).toEqual(expected[type] ?? {});
    });

    it('takes its display title from the humanized id, as the editor does', () => {
      const words = type.replace(/-/g, ' ');
      expect(descriptor.display.title).toBe(words.charAt(0).toUpperCase() + words.slice(1));
    });

    it('renders with the component its own module registered, through the public defineWidget', () => {
      const definition = builtinWidgetDefinitions.find((d) => d.descriptor.id === type);
      expect(definition, `no registration for ${type}`).toBeDefined();
      expect(definition?.descriptor).toBe(builtinWidgetDescriptors[type]);
      expect(registry.implementation(type)?.component).toBeTypeOf('function');
      expect(registry.implementation(type)?.component).toBe(definition?.implementation.component);
      expect(registry.implementation(type)?.kind).toBe('trusted-react');
    });

    // The editor now takes a new widget's options FROM the descriptor, so comparing the two
    // would be circular. These are the starting options the editor produced before it did —
    // written out, so a changed default is a decision rather than a side effect.
    it('starts a new widget with the options the editor has always given it', () => {
      const { definition } = addWidget(empty, descriptor);
      const legacy: Record<string, Record<string, unknown>> = {
        label: { text: 'New label' },
        'alarm-table': { title: 'Alarm table', state: 'ACTIVE' },
        'alarm-count': { title: 'Alarm count', state: 'ACTIVE' },
        'alarm-severity': { title: 'Alarm severity', state: 'ACTIVE' },
      };
      const words = type.replace(/-/g, ' ');
      const expected = legacy[type] ?? { title: words.charAt(0).toUpperCase() + words.slice(1) };
      expect(definition.widgets[0].options).toEqual(expected);
      expect(definition.widgets[0].type).toBe(type);
    });

    it('carries a per-widget version, not the platform version', () => {
      expect(descriptor.version).toBe('1.0.0');
      // The timeseries chart's settings changed meaning when its window became a span.
      expect(descriptor.optionsVersion).toBe(type === 'timeseries-chart' ? 2 : 1);
      expect(descriptor.sdkApiVersion).toBe(1);
    });
  });

  it('uses the policy for alarm and command page sizes the renderer applies today', () => {
    expect(registry.descriptor('alarm-count')?.binding).toMatchObject({ pageSize: { const: 50 } });
    expect(registry.descriptor('alarm-table')?.binding).toMatchObject({
      pageSize: { option: 'maxRows', default: 50, min: 1 },
    });
    expect(registry.descriptor('command-button')?.binding).toMatchObject({
      pageSize: { option: 'maxRows', default: 20, min: 1 },
    });
    expect(registry.descriptor('command-button')?.datasource).toEqual({
      mode: 'required',
      selectors: ['device', 'slot'],
    });
  });

  // The by-id views are read-only projections, not a second registry: a consumer that writes
  // an entry into one (to "register" a widget the cheap way) must fail, not half-work.
  it('serves its by-id views frozen, over exactly the built-in ids', () => {
    for (const [name, view] of Object.entries({ WIDGET_CHANNEL, WIDGET_BINDS_DATASOURCE, WIDGET_OPTIONS })) {
      expect(Object.isFrozen(view), `${name} is mutable`).toBe(true);
      expect(Object.keys(view).sort(), name).toEqual([...WIDGET_TYPES].sort());
      expect(() => {
        (view as Record<string, unknown>)['acme/tank'] = 'measurement';
      }, name).toThrow(TypeError);
    }
  });

  it('puts the static widgets on the none channel: no datasource, no policy', () => {
    for (const id of ['label', 'image'] as const) {
      expect(registry.descriptor(id)).toMatchObject({ channel: 'none', datasource: { mode: 'none' }, binding: {} });
    }
  });

  it('declares the gauge min/max rule as data on the descriptor', () => {
    expect(registry.descriptor('gauge')?.constraints).toEqual([{ kind: 'greater-than', key: 'max', than: 'min' }]);
    // And it is JSON: nothing in a built-in descriptor is a function.
    for (const d of registry.catalog.list()) {
      expect(JSON.parse(JSON.stringify(d))).toEqual(d);
    }
  });

  it('is independent per call', () => {
    const other = createBuiltinWidgetRegistry();
    expect(other).not.toBe(registry);
    expect(other.list().map((d) => d.descriptor.id)).toEqual(registry.list().map((d) => d.descriptor.id));
  });

  it('is the schema the live publish-gate validation reads', () => {
    // validateDefinitionOptions resolves schemas AND constraints through the built-in
    // catalog; a defect there fails the existing publish/validation tests. This pins that it
    // is the catalog's constraint (data), not a closure, producing the invariant.
    const issues = validateDefinitionOptions({
      widgets: [{ id: 'g', type: 'gauge', layout: {}, options: { min: 10, max: 5 } }],
    } as never);
    expect(issues).toEqual([
      expect.objectContaining({
        widgetId: 'g',
        widgetType: 'gauge',
        key: 'max',
        code: 'invariant',
        message: 'max (5) must be greater than min (10)',
      }),
    ]);
  });
});

describe('the publish gate', () => {
  // Every built-in's catalog schema is the WIDGET_OPTIONS view by identity, so a gate that
  // quietly went back to the view would produce identical issues and pass every behavioural
  // test — while the catalog stopped being on any live path. Pin the source instead.
  it('resolves schemas through the catalog, not the by-type views', () => {
    const src = dirname(fileURLToPath(import.meta.url));
    const code = readFileSync(join(src, 'definition-options.ts'), 'utf8')
      .split('\n')
      .filter((line) => !line.trim().startsWith('//'))
      .join('\n');
    expect(code).toMatch(/builtinWidgetCatalog\.get\(/);
    expect(code).not.toMatch(/\b(WIDGET_OPTIONS|WIDGET_CONSTRAINTS|validateWidgetOptions|numberOptionSpec)\b/);
    // Anything still imported from ./options is a type only.
    expect(code).not.toMatch(/^import\s+\{[^}]*\}\s+from\s+'\.\/options'/m);
  });
});

describe('ambient contexts', () => {
  // A React context is its createContext object, so a second definition in this package
  // would be a context the host's provider never writes to. The contexts live in the SDK
  // alone; this package re-exports them from its index.
  it('are not redefined in the widgets package, and the index re-exports the SDK ones', async () => {
    const src = dirname(fileURLToPath(import.meta.url));
    // Only the five contexts the SDK owns; this package has its own (basemap, map runtime).
    const redefined = sourcesUnder(src).filter((f) =>
      /Widget(?:Subject|TimeZone|ThemeRoot|Select|Candidates)Context\s*=\s*createContext/.test(readFileSync(f, 'utf8')),
    );
    expect(redefined).toEqual([]);
    const sdk = await import('@devicechain/widget-sdk');
    const widgets = await import('./index');
    expect(widgets.useWidgetSelect).toBe(sdk.useWidgetSelect);
    expect(widgets.WidgetCandidatesProvider).toBe(sdk.WidgetCandidatesProvider);
    expect(widgets.useWidgetTimeZone).toBe(sdk.useWidgetTimeZone);
  });
});

function sourcesUnder(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourcesUnder(full));
    else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(full);
  }
  return out;
}

describe('the built-in size contract', () => {
  it('declares a layout for every built-in', () => {
    for (const type of WIDGET_TYPES) {
      expect(registry.descriptor(type)?.layout, `${type} declares no layout`).toBeDefined();
    }
  });

  it('keeps each default inside its own limits, as the lint reads them', () => {
    // A freshly added widget must never lint as out of limits.
    for (const type of WIDGET_TYPES) {
      const { defaultSpan } = registry.descriptor(type)!.layout!;
      const { minSpan = {}, maxSpan = {} } = spanLimitsOfLayout(registry.descriptor(type)!.layout)!;
      expect(defaultSpan.cols, type).toBeGreaterThanOrEqual(minSpan.cols ?? 1);
      expect(defaultSpan.rows, type).toBeGreaterThanOrEqual(minSpan.rows ?? 1);
      expect(defaultSpan.cols, type).toBeLessThanOrEqual(maxSpan.cols ?? Infinity);
      expect(defaultSpan.rows, type).toBeLessThanOrEqual(maxSpan.rows ?? Infinity);
    }
  });

  it('adds each built-in at its own default span', () => {
    for (const type of WIDGET_TYPES) {
      const descriptor = registry.descriptor(type)!;
      const { definition } = addWidget(empty, descriptor);
      const placement = basePlacement(definition.widgets[0]);
      expect(placement.mode === 'grid' && { cols: placement.cols, rows: placement.rows }, type).toEqual(
        descriptor.layout!.defaultSpan,
      );
    }
  });
});
