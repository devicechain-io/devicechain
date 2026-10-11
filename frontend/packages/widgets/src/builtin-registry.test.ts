// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// The built-in registry is built BY REFERENCE from today's tables, and it sits on one live
// path (the publish-gate option validation). This file is what stops it rotting: every
// assertion compares the registry to the table it was derived from, over EVERY widget type,
// so a swapped component, a changed channel or a drifted default is a red test rather than a
// registry that quietly disagrees with the renderer.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { WIDGET_TYPES, addWidget, type WidgetType } from '@devicechain/dashboards';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { describe, expect, it } from 'vitest';

import { builtinWidgetCatalog } from './builtin-descriptors';
import { builtinWidgetDefinitions, createBuiltinWidgetRegistry } from './builtin-registry';
import { validateDefinitionOptions } from './definition-options';
import { WIDGET_OPTIONS } from './options';
import {
  ALARM_WIDGET_REGISTRY,
  CONTROL_WIDGET_REGISTRY,
  LOCATION_WIDGET_REGISTRY,
  SELECTION_WIDGET_REGISTRY,
  WIDGET_BINDS_DATASOURCE,
  WIDGET_CHANNEL,
  WIDGET_REGISTRY,
} from './registry';

// The component the per-channel registries hold for a type, looked up the way
// ConnectedWidget does today.
function tableComponent(type: WidgetType): unknown {
  switch (WIDGET_CHANNEL[type]) {
    case 'alarm':
      return ALARM_WIDGET_REGISTRY[type as keyof typeof ALARM_WIDGET_REGISTRY];
    case 'control':
      return CONTROL_WIDGET_REGISTRY[type as keyof typeof CONTROL_WIDGET_REGISTRY];
    case 'selection':
      return SELECTION_WIDGET_REGISTRY[type as keyof typeof SELECTION_WIDGET_REGISTRY];
    case 'location':
      return LOCATION_WIDGET_REGISTRY[type as keyof typeof LOCATION_WIDGET_REGISTRY];
    default:
      return WIDGET_REGISTRY[type as keyof typeof WIDGET_REGISTRY];
  }
}

const registry = createBuiltinWidgetRegistry();
const empty = { version: 1, widgets: [] } as never;

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

    it('takes its channel and option schema by identity from the tables', () => {
      expect(descriptor.channel).toBe(WIDGET_CHANNEL[type]);
      expect(descriptor.options).toBe(WIDGET_OPTIONS[type]);
    });

    it('binds a datasource exactly when the table says it does', () => {
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
        selectors: channel === 'control' ? ['device', 'slot'] : ['device', 'anchor', 'slot'],
      });
    });

    // Each declared interaction is one the component actually uses (alarm-table acts on and
    // drills from alarms, command-button sends, entity-selector picks from candidates).
    it('declares exactly the interactions its component uses', () => {
      const expected: Partial<Record<WidgetType, object>> = {
        'alarm-table': { alarmActions: true, selection: true },
        'command-button': { commandSend: true },
        'entity-selector': { selection: true, candidates: true },
      };
      expect(descriptor.interactions).toEqual(expected[type] ?? {});
    });

    it('takes its display title from the humanized id, as the editor does', () => {
      const words = type.replace(/-/g, ' ');
      expect(descriptor.display.title).toBe(words.charAt(0).toUpperCase() + words.slice(1));
    });

    it('renders with the component the per-channel registry holds', () => {
      const component = tableComponent(type);
      expect(component).toBeTypeOf('function');
      expect(registry.implementation(type)?.component).toBe(component);
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
      };
      const words = type.replace(/-/g, ' ');
      const expected = legacy[type] ?? { title: words.charAt(0).toUpperCase() + words.slice(1) };
      expect(definition.widgets[0].options).toEqual(expected);
      expect(definition.widgets[0].type).toBe(type);
    });

    it('carries a per-widget version, not the platform version', () => {
      expect(descriptor.version).toBe('1.0.0');
      expect(descriptor.optionsVersion).toBe(1);
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
  // Every built-in's catalog schema is the WIDGET_OPTIONS table by identity, so a gate that
  // quietly went back to the table would produce identical issues and pass every behavioural
  // test — while the catalog stopped being on any live path. Pin the source instead.
  it('resolves schemas through the catalog, not the by-type tables', () => {
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
  // would be a context the host's provider never writes to. The contexts live in the SDK;
  // frame.tsx must only re-export them.
  it('are not redefined in the widgets package', async () => {
    const src = dirname(fileURLToPath(import.meta.url));
    const frame = readFileSync(join(src, 'frame.tsx'), 'utf8');
    expect(frame).not.toMatch(/createContext\s*[<(]/);
    const sdk = await import('@devicechain/widget-sdk');
    const frameModule = await import('./frame');
    expect(frameModule.useWidgetSelect).toBe(sdk.useWidgetSelect);
    expect(frameModule.WidgetCandidatesProvider).toBe(sdk.WidgetCandidatesProvider);
    expect(frameModule.useWidgetTimeZone).toBe(sdk.useWidgetTimeZone);
  });
});
