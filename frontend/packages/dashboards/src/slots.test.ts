// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition } from './definition';
import {
  anchorSlotNames,
  bindWidgetSlot,
  clearWidgetDatasource,
  migrateToSlots,
  pruneSlots,
  resolveConcrete,
  sameBinding,
  setSlotScope,
  widgetBinding,
} from './slots';
import type { DashboardDefinition, DatasourceSelector, SlotBinding, SlotDefinition, WidgetInstance } from './types';

const anchorSel = (targetToken: string, extra: Partial<DatasourceSelector> = {}): DatasourceSelector =>
  ({
    kind: 'anchor',
    anchor: { relationship: 'contains', targetType: 'area', targetToken },
    measurements: ['t'],
    ...extra,
  }) as DatasourceSelector;

const box = { col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0 };

function widget(id: string, datasource?: WidgetInstance['datasource']): WidgetInstance {
  return { id, type: 'gauge', layout: { base: box }, ...(datasource ? { datasource } : {}) };
}

function def(widgets: WidgetInstance[], slots?: DashboardDefinition['slots']): DashboardDefinition {
  return {
    schemaVersion: 1,
    title: 'T',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets,
    ...(slots ? { slots } : {}),
  };
}

const devA: SlotBinding = { kind: 'device', deviceToken: 'therm-001' };

describe('sameBinding', () => {
  it('compares device and anchor bindings by value', () => {
    expect(sameBinding(devA, { kind: 'device', deviceToken: 'therm-001' })).toBe(true);
    expect(sameBinding(devA, { kind: 'device', deviceToken: 'other' })).toBe(false);
    const a: SlotBinding = { kind: 'anchor', anchor: { relationship: 'r', targetType: 'area', targetToken: 'x' } };
    expect(sameBinding(a, { kind: 'anchor', anchor: { relationship: 'r', targetType: 'area', targetToken: 'x' } })).toBe(true);
    expect(sameBinding(a, devA)).toBe(false);
  });
});

describe('migrateToSlots', () => {
  it('rewrites concrete selectors to slots and dedups identical bindings', () => {
    const d = def([
      widget('a', { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] }),
      widget('b', { kind: 'device', deviceToken: 'therm-001', measurements: ['humidity'] }),
      widget('c', { kind: 'device', deviceToken: 'other', measurements: [] }),
    ]);
    const m = migrateToSlots(d);
    // a + b share one slot (same device); c gets its own.
    expect(Object.keys(m.slots!)).toHaveLength(2);
    expect(m.widgets[0].datasource).toEqual({ kind: 'slot', slot: 'slot-1', measurements: ['temperature'] });
    expect(m.widgets[1].datasource).toEqual({ kind: 'slot', slot: 'slot-1', measurements: ['humidity'] });
    expect(m.widgets[2].datasource).toEqual({ kind: 'slot', slot: 'slot-2', measurements: [] });
    expect(m.slots!['slot-1']).toEqual({ type: 'device', label: 'therm-001', defaultBinding: devA });
  });

  it('is idempotent — a slot-based definition passes through unchanged', () => {
    const d = migrateToSlots(
      def([widget('a', { kind: 'device', deviceToken: 'therm-001', measurements: ['t'] })]),
    );
    expect(migrateToSlots(d)).toEqual(d);
  });

  it('leaves a slot-free (label/image only) dashboard untouched, no slots key', () => {
    const d = def([widget('a')]); // no datasource
    const m = migrateToSlots(d);
    expect('slots' in m).toBe(false);
    expect(m).toBe(d); // same reference — no work done
  });

  it('migrates an anchor selector and round-trips through serialize/parse', () => {
    const m = migrateToSlots(def([widget('a', anchorSel('plant-1'))]));
    expect(m.widgets[0].datasource).toEqual({ kind: 'slot', slot: 'slot-1', measurements: ['t'] });
    expect(m.slots!['slot-1']).toEqual({
      type: 'anchor',
      label: 'plant-1',
      defaultBinding: { kind: 'anchor', anchor: { relationship: 'contains', targetType: 'area', targetToken: 'plant-1' } },
    });
    // The default binding survives a serialize→parse round-trip (the D1 data-loss guard).
    expect(parseDashboardDefinition(JSON.parse(serializeDefinition(m)))).toEqual(m);
  });

  it('leaves a partial anchor (no target token) as a concrete selector, no data loss', () => {
    const d = def([widget('a', anchorSel(''))]);
    const m = migrateToSlots(d);
    expect(m).toBe(d); // nothing complete to migrate → same reference, no slots:{}
  });

  it('leaves an aggregated anchor concrete (slot model has no aggregation)', () => {
    const d = def([widget('a', anchorSel('plant-1', { aggregation: { window: '1m', fn: 'avg' } }))]);
    const m = migrateToSlots(d);
    expect(m).toBe(d);
  });

  it('does not attach a slots:{} for an empty-token device (nothing to migrate)', () => {
    const d = def([widget('a', { kind: 'device', deviceToken: '', measurements: [] })]);
    const m = migrateToSlots(d);
    expect(m).toBe(d);
    expect('slots' in m).toBe(false);
  });
});

describe('bindWidgetSlot — duplicate-binding slots', () => {
  it('keeps the widget on its own slot for a measurements-only edit (no rehome)', () => {
    // Two slots share the SAME device binding but distinct identities (a valid import /
    // I-3 template shape). Editing widget B's measurements must NOT collapse it onto A's.
    const d = def([widget('a', { kind: 'slot', slot: 'slot-1', measurements: [] }), widget('b', { kind: 'slot', slot: 'slot-2', measurements: [] })], {
      'slot-1': { type: 'device', defaultBinding: devA },
      'slot-2': { type: 'device', defaultBinding: devA },
    });
    const next = bindWidgetSlot(d, 'b', devA, ['humidity']);
    expect(next.widgets[1].datasource).toEqual({ kind: 'slot', slot: 'slot-2', measurements: ['humidity'] });
    expect(Object.keys(next.slots!)).toEqual(['slot-1', 'slot-2']); // slot-2 preserved
  });
});

describe('bindWidgetSlot', () => {
  it('creates a slot for a new binding and points the widget at it', () => {
    const d = def([widget('a', { kind: 'slot', slot: 'slot-1', measurements: [] })], {
      'slot-1': { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'old' } },
    });
    const next = bindWidgetSlot(d, 'a', devA, ['temperature']);
    // A new binding forks a new slot; the old one is left (prune reclaims it).
    expect(next.widgets[0].datasource).toEqual({ kind: 'slot', slot: 'slot-2', measurements: ['temperature'] });
    expect(next.slots!['slot-2'].defaultBinding).toEqual(devA);
  });

  it('reuses an existing slot with the same binding (dedup)', () => {
    const d = def([widget('a', { kind: 'slot', slot: 'slot-1', measurements: [] }), widget('b')], {
      'slot-1': { type: 'device', defaultBinding: devA },
    });
    const next = bindWidgetSlot(d, 'b', devA, ['humidity']);
    expect(next.widgets[1].datasource).toEqual({ kind: 'slot', slot: 'slot-1', measurements: ['humidity'] });
    expect(Object.keys(next.slots!)).toEqual(['slot-1']); // no new slot
  });
});

describe('pruneSlots', () => {
  it('drops unreferenced slots and omits the key when empty', () => {
    const d = def([widget('a', { kind: 'slot', slot: 'slot-1', measurements: [] })], {
      'slot-1': { type: 'device', defaultBinding: devA },
      'slot-2': { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'orphan' } },
    });
    const pruned = pruneSlots(d);
    expect(Object.keys(pruned.slots!)).toEqual(['slot-1']);

    const cleared = pruneSlots(clearWidgetDatasource(d, 'a'));
    expect('slots' in cleared).toBe(false);
  });
});

describe('widgetBinding / resolveConcrete', () => {
  it('resolves a slot widget through its default binding', () => {
    const d = def([widget('a', { kind: 'slot', slot: 'slot-1', measurements: ['t'] })], {
      'slot-1': { type: 'device', defaultBinding: devA },
    });
    expect(widgetBinding(d, d.widgets[0])).toEqual(devA);
    expect(resolveConcrete(d, d.widgets[0])).toEqual({
      kind: 'device',
      deviceToken: 'therm-001',
      measurements: ['t'],
    });
  });

  it('returns undefined for an unbound slot or a datasource-less widget', () => {
    const d = def([widget('a', { kind: 'slot', slot: 'ghost', measurements: [] }), widget('b')]);
    expect(resolveConcrete(d, d.widgets[0])).toBeUndefined();
    expect(resolveConcrete(d, d.widgets[1])).toBeUndefined();
  });
});

// Scope-aware helper fixes (ADR-039 selection amendment): dedup must not fold a plain
// binding onto a scoped slot, and prune must keep a context-only parent slot.
describe('scope-aware slot helpers', () => {
  const deviceSel = (token: string): DatasourceSelector =>
    ({ kind: 'device', deviceToken: token, measurements: ['t'] }) as DatasourceSelector;
  const areaBinding: SlotBinding = {
    kind: 'anchor',
    anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'b1' },
  };

  it('migrateToSlots does not fold a plain device selector onto a scoped slot sharing its binding', () => {
    const d = def([widget('w1', deviceSel('therm-001'))], {
      building: { type: 'anchor', defaultBinding: areaBinding },
      selected: { type: 'device', defaultBinding: devA, scope: { parent: 'building', strategy: 'manual' } },
    });
    const m = migrateToSlots(d);
    const ds = m.widgets[0].datasource as { kind: string; slot: string };
    expect(ds.kind).toBe('slot');
    expect(ds.slot).not.toBe('selected'); // a fresh PLAIN slot, not the scoped one
    expect(m.slots![ds.slot].scope).toBeUndefined();
  });

  it('pruneSlots keeps a context-only parent slot that only a scoped child references', () => {
    const d = def([widget('w1', { kind: 'slot', slot: 'therm', measurements: ['t'] })], {
      building: { type: 'anchor', defaultBinding: areaBinding },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'first' } },
    });
    const pruned = pruneSlots(d);
    // `building` is kept even though no widget binds it directly — the child's scope.parent
    // closure keeps it, or the cascade would lose its top-level context.
    expect(Object.keys(pruned.slots!).sort()).toEqual(['building', 'therm']);
  });
});

// slot definition helper for the scope-authoring transforms.
const scopeDef = (slots: Record<string, SlotDefinition>): DashboardDefinition => ({
  schemaVersion: 1,
  title: '',
  canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [],
  slots,
});

describe('anchorSlotNames', () => {
  it('lists only anchor-typed slots', () => {
    const def = scopeDef({
      building: { type: 'anchor' },
      floor: { type: 'anchor' },
      therm: { type: 'device' },
    });
    expect(anchorSlotNames(def).sort()).toEqual(['building', 'floor']);
  });

  it('is empty for a slot-free dashboard', () => {
    expect(anchorSlotNames(scopeDef({}))).toEqual([]);
  });
});

describe('setSlotScope', () => {
  it('scopes a device slot to an anchor parent', () => {
    const def = scopeDef({ building: { type: 'anchor' }, therm: { type: 'device' } });
    const next = setSlotScope(def, 'therm', { parent: 'building', strategy: 'manual' });
    expect(next.slots!.therm.scope).toEqual({ parent: 'building', strategy: 'manual' });
  });

  it('clears a scope back to root', () => {
    const def = scopeDef({
      building: { type: 'anchor' },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'first' } },
    });
    const next = setSlotScope(def, 'therm', undefined);
    expect(next.slots!.therm.scope).toBeUndefined();
  });

  it('no-ops (same reference) clearing an already-root slot', () => {
    const def = scopeDef({ therm: { type: 'device' } });
    expect(setSlotScope(def, 'therm', undefined)).toBe(def);
  });

  it('rejects a non-anchor parent (no-op)', () => {
    const def = scopeDef({ gw: { type: 'device' }, therm: { type: 'device' } });
    expect(setSlotScope(def, 'therm', { parent: 'gw', strategy: 'first' })).toBe(def);
  });

  it('rejects a self-referential scope (no-op)', () => {
    const def = scopeDef({ a: { type: 'anchor' } });
    expect(setSlotScope(def, 'a', { parent: 'a', strategy: 'first' })).toBe(def);
  });

  it('rejects a scope that would create a cycle (no-op)', () => {
    // a←b already; scoping b's parent to a's descendant (a→b) would loop.
    const def = scopeDef({
      a: { type: 'anchor', scope: { parent: 'b', strategy: 'first' } },
      b: { type: 'anchor' },
    });
    expect(setSlotScope(def, 'b', { parent: 'a', strategy: 'first' })).toBe(def);
  });

  it('no-ops on an unknown slot', () => {
    const def = scopeDef({ building: { type: 'anchor' } });
    expect(setSlotScope(def, 'nope', { parent: 'building', strategy: 'first' })).toBe(def);
  });
});
