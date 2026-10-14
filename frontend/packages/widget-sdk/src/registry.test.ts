// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// createWidgetRegistry's runtime rejections. The type-negative fixtures prove the compiler
// refuses these; this file proves the REGISTRY refuses them too, for callers that bypass the
// compiler (plain JS, JSON-derived descriptors, a cast). Each case asserts the issue id and
// PATH — "it threw" would pass for a registry that complained about the wrong field.

import { memo } from 'react';
import { describe, expect, it } from 'vitest';

import type { MeasurementStreamState } from '@devicechain/dashboards';

import {
  WidgetCatalogError,
  WidgetRegistryError,
  createWidgetRegistry,
  defineWidget,
  type DescriptorIssue,
  type WidgetDescriptor,
  type ErasedWidgetDefinition,
  type WidgetProps,
} from './index';

const OPTIONS = {
  title: { kind: 'string', doc: 'Heading.' },
  min: { kind: 'number', doc: 'Low.' },
  max: { kind: 'number', doc: 'High.' },
  window: { kind: 'number', integer: true, min: 1, doc: 'Window.' },
} as const;

const component = (_props: WidgetProps<MeasurementStreamState>) => null;

// A definition with fields overwritten, deliberately outside the type system: these are the
// shapes JSON and JS callers can produce that the compiler never sees (the type-negative
// fixtures own the compile-time half). One well-typed defineWidget call keeps the happy path
// honest.
const tank = (over: Record<string, unknown> = {}): ErasedWidgetDefinition => ({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.2.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1 } },
    options: OPTIONS,
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Tank', min: 0, max: 100 },
    interactions: {},
    ...over,
  } as unknown as WidgetDescriptor,
  implementation: { kind: 'trusted-react', component },
});

const withImplementation = (implementation: unknown): ErasedWidgetDefinition =>
  ({ descriptor: tank().descriptor, implementation }) as ErasedWidgetDefinition;

function issuesFor(definitions: unknown[]): DescriptorIssue[] {
  try {
    createWidgetRegistry(definitions as ErasedWidgetDefinition[]);
  } catch (error) {
    expect(error).toBeInstanceOf(WidgetRegistryError);
    // A registry error IS a catalog error, so one handler covers both.
    expect(error).toBeInstanceOf(WidgetCatalogError);
    return [...(error as WidgetRegistryError).issues];
  }
  throw new Error('expected createWidgetRegistry to throw');
}

const keys = (issues: DescriptorIssue[]) => issues.map((i) => `${i.id}:${i.path}`);

describe('createWidgetRegistry', () => {
  it('builds a registry from a valid definition', () => {
    const typed = defineWidget({
      descriptor: { ...tank().descriptor, channel: 'measurement' } as WidgetDescriptor<'measurement', typeof OPTIONS>,
      implementation: { kind: 'trusted-react', component },
    });
    const registry = createWidgetRegistry([typed]);
    expect(registry.list().map((d) => d.descriptor.id)).toEqual(['acme/tank-level']);
    expect(registry.descriptor('acme/tank-level')?.version).toBe('1.2.0');
    expect(registry.implementation('acme/tank-level')?.component).toBe(component);
    expect(registry.catalog.get('acme/tank-level')).toBe(registry.descriptor('acme/tank-level'));
    expect(registry.descriptor('acme/missing')).toBeUndefined();
    expect(registry.implementation('acme/missing')).toBeUndefined();
  });

  it('rejects a duplicate id, including two built-in gauges', () => {
    expect(keys(issuesFor([tank(), tank()]))).toEqual(['acme/tank-level:id']);
    expect(keys(issuesFor([tank({ id: 'gauge' }), tank({ id: 'gauge' })]))).toEqual(['gauge:id']);
  });

  it('accepts a reserved id once, and rejects a non-namespaced unknown id', () => {
    expect(() => createWidgetRegistry([tank({ id: 'gauge' })])).not.toThrow();
    expect(keys(issuesFor([tank({ id: 'tank-level' })]))).toEqual(['tank-level:id']);
  });

  it('rejects a malformed semver', () => {
    expect(keys(issuesFor([tank({ version: '1.2' })]))).toEqual(['acme/tank-level:version']);
  });

  it('rejects an unsupported sdkApiVersion', () => {
    expect(keys(issuesFor([tank({ sdkApiVersion: 2 })]))).toEqual(['acme/tank-level:sdkApiVersion']);
  });

  it('rejects a non-JSON value in the descriptor', () => {
    const issues = issuesFor([tank({ display: { title: 'T', render: () => null } })]);
    expect(keys(issues)).toEqual(['acme/tank-level:display.render']);
  });

  it('rejects invalid defaults, including a constraint violation', () => {
    expect(keys(issuesFor([tank({ defaults: { window: 0 } })]))).toEqual(['acme/tank-level:defaults.window']);
    const constraint = issuesFor([tank({ defaults: { min: 5, max: 1 } })]);
    expect(keys(constraint)).toEqual(['acme/tank-level:defaults.max']);
    expect(constraint[0].message).toBe('max (1) must be greater than min (5)');
  });

  it('rejects a binding that names a missing key or a key of the wrong kind', () => {
    expect(keys(issuesFor([tank({ binding: { window: { option: 'ghost' } } })]))).toEqual([
      'acme/tank-level:binding.window.option',
    ]);
    expect(keys(issuesFor([tank({ binding: { window: { option: 'title' } } })]))).toEqual([
      'acme/tank-level:binding.window.option',
    ]);
  });

  it('rejects a channel/datasource mismatch', () => {
    expect(keys(issuesFor([tank({ channel: 'none', binding: {} })]))).toEqual(['acme/tank-level:datasource.mode']);
  });

  it('rejects a channel/interaction mismatch', () => {
    expect(keys(issuesFor([tank({ interactions: { commandSend: true } })]))).toEqual([
      'acme/tank-level:interactions.commandSend',
    ]);
  });

  it('validates a third-party layout block, and accepts one that is well formed or absent', () => {
    const layout = (l: unknown) => keys(issuesFor([tank({ layout: l })]));
    expect(layout({ minSpan: { cols: 2, rows: 1 } })).toEqual(['acme/tank-level:layout.defaultSpan']);
    expect(layout({ defaultSpan: { cols: 0, rows: 2 } })).toEqual(['acme/tank-level:layout.defaultSpan.cols']);
    expect(layout({ defaultSpan: { cols: 4, rows: 2 }, maxSpan: { cols: 4, rows: 1 } })).toEqual([
      'acme/tank-level:layout.maxSpan',
    ]);
    expect(layout({ defaultSpan: { cols: 4, rows: 2 }, maxSpan: { cols: 8, rows: 4 }, resize: 'horizontal' })).toEqual([
      'acme/tank-level:layout.resize',
    ]);
    expect(layout({ defaultSpan: { cols: 4, rows: 2 }, gap: 1 })).toEqual(['acme/tank-level:layout.gap']);
    const ok = { defaultSpan: { cols: 4, rows: 2 }, minSpan: { cols: 2, rows: 2 }, resize: 'horizontal' };
    expect(createWidgetRegistry([tank({ layout: ok })]).descriptor('acme/tank-level')?.layout).toEqual(ok);
    expect(createWidgetRegistry([tank()]).descriptor('acme/tank-level')?.layout).toBeUndefined();
  });

  it('rejects a bad implementation (kind, component, editor, missing)', () => {
    const at = (impl: unknown) => keys(issuesFor([withImplementation(impl)]));
    expect(at({ kind: 'sandboxed-iframe', component })).toEqual(['acme/tank-level:implementation.kind']);
    expect(at({ kind: 'trusted-react', component: 'nope' })).toEqual(['acme/tank-level:implementation.component']);
    expect(at({ kind: 'trusted-react', component, editor: 3 })).toEqual(['acme/tank-level:implementation.editor']);
    expect(at(undefined)).toEqual(['acme/tank-level:implementation']);
    // memo/forwardRef components are objects tagged with $$typeof, and are fine.
    expect(() => createWidgetRegistry([withImplementation({ kind: 'trusted-react', component: memo(component) })])).not.toThrow();
  });

  it('reports every fault together, across descriptors and implementations', () => {
    const issues = issuesFor([
      tank({ version: 'x', sdkApiVersion: 2 }),
      tank({ id: 'acme/second', defaults: { title: 7 } }),
      tank({ id: 'acme/third' }),
      { descriptor: tank({ id: 'acme/fourth' }).descriptor, implementation: { kind: 'wasm', component } },
      tank({ id: 'acme/third' }),
    ]);
    expect(keys(issues)).toEqual([
      'acme/tank-level:version',
      'acme/tank-level:sdkApiVersion',
      'acme/second:defaults.title',
      'acme/third:id',
      'acme/fourth:implementation.kind',
    ]);
  });

  it('rejects input that is not a list of definitions', () => {
    expect(() => createWidgetRegistry(null as never)).toThrow(WidgetRegistryError);
    expect(keys(issuesFor([null]))).toEqual(['#0:', '#0:implementation']);
  });

  it('puts the failing ids and paths in the message', () => {
    expect(() => createWidgetRegistry([tank({ version: 'x' })])).toThrow(/acme\/tank-level\.version/);
  });

  it('returns a frozen registry', () => {
    const input = tank();
    const registry = createWidgetRegistry([input]);
    expect(Object.isFrozen(registry)).toBe(true);
    expect(Object.isFrozen(registry.list())).toBe(true);
    expect(Object.isFrozen(registry.list()[0])).toBe(true);
    expect(Object.isFrozen(registry.implementation('acme/tank-level'))).toBe(true);
    expect(Object.isFrozen(registry.descriptor('acme/tank-level'))).toBe(true);
    expect(() => {
      (registry as { catalog: unknown }).catalog = null;
    }).toThrow(TypeError);
    expect(() => {
      (registry.implementation('acme/tank-level') as { kind: string }).kind = 'other';
    }).toThrow(TypeError);
    expect(() => {
      (registry.list() as ErasedWidgetDefinition[]).push(input);
    }).toThrow(TypeError);
    // The caller's own implementation object was copied, not frozen out from under them.
    expect(Object.isFrozen(input.implementation)).toBe(false);
  });

  it('keeps two registries independent', () => {
    const a = createWidgetRegistry([tank()]);
    const b = createWidgetRegistry([tank({ id: 'acme/other' })]);

    // An extension in A is not visible to B or to B's catalog, and vice versa.
    expect(a.descriptor('acme/other')).toBeUndefined();
    expect(a.catalog.has('acme/other')).toBe(false);
    expect(b.descriptor('acme/tank-level')).toBeUndefined();
    expect(b.catalog.has('acme/tank-level')).toBe(false);
    expect(b.implementation('acme/tank-level')).toBeUndefined();

    // Constructing B (and a failing C) left A unchanged.
    expect(() => createWidgetRegistry([tank({ version: 'x' })])).toThrow();
    expect(a.list().map((d) => d.descriptor.id)).toEqual(['acme/tank-level']);
    expect(a.catalog.list().map((d) => d.id)).toEqual(['acme/tank-level']);
  });

  it('lets two registries each register the same id', () => {
    // No process-global registration: the id is only unique WITHIN a registry.
    expect(() => {
      createWidgetRegistry([tank()]);
      createWidgetRegistry([tank()]);
    }).not.toThrow();
  });
});
