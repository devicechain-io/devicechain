// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';

import { applySelection, hasScopedSlots, resolveContextBindings, type MemberResolver } from './context';
import type { AnchorTarget, DashboardDefinition, SlotBinding, SlotDefinition } from './types';

const anchor = (targetToken: string): AnchorTarget => ({
  relationship: 'assigned',
  targetType: 'area',
  targetToken,
});

const anchorBinding = (targetToken: string): SlotBinding => ({ kind: 'anchor', anchor: anchor(targetToken) });
const deviceBinding = (deviceToken: string): SlotBinding => ({ kind: 'device', deviceToken });

function def(slots: Record<string, SlotDefinition>): DashboardDefinition {
  return {
    schemaVersion: 1,
    title: 'T',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [],
    slots,
  };
}

// A resolver whose membership is a fixed map building-token → member device tokens.
function memberResolver(members: Record<string, string[]>): MemberResolver {
  return {
    devicesForAnchor: vi.fn(async (a: AnchorTarget) => members[a.targetToken] ?? []),
  };
}

// The buildingpulse-shaped hierarchy: a root `building` anchor + a `selectedThermostat`
// device scoped to it (strategy configurable).
function hierarchy(strategy: 'first' | 'manual', buildingToken = 'b1'): DashboardDefinition {
  return def({
    building: { type: 'anchor', defaultBinding: anchorBinding(buildingToken) },
    selectedThermostat: { type: 'device', scope: { parent: 'building', strategy } },
  });
}

describe('hasScopedSlots', () => {
  it('is false with no slots or no scoped slot, true once a slot is scoped', () => {
    expect(hasScopedSlots(def({}))).toBe(false);
    expect(hasScopedSlots(def({ a: { type: 'anchor' } }))).toBe(false);
    expect(hasScopedSlots(hierarchy('first'))).toBe(true);
  });
});

describe('applySelection', () => {
  it('folds a pick into the overlay without mutating', () => {
    const before = { s1: deviceBinding('d1') };
    const after = applySelection(before, { slot: 's2', binding: deviceBinding('d2') });
    expect(before).toEqual({ s1: deviceBinding('d1') }); // unchanged
    expect(after).toEqual({ s1: deviceBinding('d1'), s2: deviceBinding('d2') });
  });
});

describe('resolveContextBindings', () => {
  it('passes a scope-free definition through (root = overlay over base)', async () => {
    const d = def({ s1: { type: 'device', defaultBinding: deviceBinding('default') } });
    const base = { s1: deviceBinding('default') };
    const out = await resolveContextBindings(d, base, {}, memberResolver({}));
    expect(out).toEqual({ s1: deviceBinding('default') });

    const overlaid = await resolveContextBindings(d, base, { s1: deviceBinding('picked') }, memberResolver({}));
    expect(overlaid).toEqual({ s1: deviceBinding('picked') }); // selection wins over base
  });

  it("first: binds the parent's first member (ordered by token), ignoring any default", async () => {
    const d = hierarchy('first');
    const base = { building: anchorBinding('b1'), selectedThermostat: deviceBinding('stale-default') };
    const resolver = memberResolver({ b1: ['t3', 't1', 't2'] });
    const out = await resolveContextBindings(d, base, {}, resolver);
    expect(out.building).toEqual(anchorBinding('b1'));
    expect(out.selectedThermostat).toEqual(deviceBinding('t1')); // sorted → t1, default ignored
  });

  it('first: zero members → the scoped slot is unbound (omitted), not an error', async () => {
    const out = await resolveContextBindings(
      hierarchy('first'),
      { building: anchorBinding('b1') },
      {},
      memberResolver({ b1: [] }),
    );
    expect(out.building).toEqual(anchorBinding('b1'));
    expect('selectedThermostat' in out).toBe(false);
  });

  it('manual: keeps a selection that is a member; drops one that is not', async () => {
    const d = hierarchy('manual');
    const base = { building: anchorBinding('b1') };
    const resolver = memberResolver({ b1: ['t1', 't2'] });

    const kept = await resolveContextBindings(d, base, { selectedThermostat: deviceBinding('t2') }, resolver);
    expect(kept.selectedThermostat).toEqual(deviceBinding('t2'));

    const dropped = await resolveContextBindings(d, base, { selectedThermostat: deviceBinding('t9') }, resolver);
    expect('selectedThermostat' in dropped).toBe(false); // t9 is not a member → unbound
  });

  it('manual: after a building switch, a now-out-of-building default drops to unbound', async () => {
    // selectedThermostat defaults to t1 (a member of b1). Select building b2 (members t8,t9);
    // t1 is no longer a member, so the manual slot resets to unbound.
    const d = def({
      building: { type: 'anchor', defaultBinding: anchorBinding('b1') },
      selectedThermostat: { type: 'device', defaultBinding: deviceBinding('t1'), scope: { parent: 'building', strategy: 'manual' } },
    });
    const base = { building: anchorBinding('b1'), selectedThermostat: deviceBinding('t1') };
    const resolver = memberResolver({ b1: ['t1', 't2'], b2: ['t8', 't9'] });

    const same = await resolveContextBindings(d, base, {}, resolver);
    expect(same.selectedThermostat).toEqual(deviceBinding('t1')); // still in b1

    const switched = await resolveContextBindings(d, base, { building: anchorBinding('b2') }, resolver);
    expect(switched.building).toEqual(anchorBinding('b2'));
    expect('selectedThermostat' in switched).toBe(false); // t1 not in b2 → unbound
  });

  it('unbound parent → the scoped child is unbound (no resolver call)', async () => {
    const resolver = memberResolver({ b1: ['t1'] });
    const out = await resolveContextBindings(hierarchy('first'), {}, {}, resolver);
    expect(out).toEqual({}); // no building binding → no child
    expect(resolver.devicesForAnchor).not.toHaveBeenCalled();
  });

  it('fail-safe: a membership error leaves the child unbound, never throws', async () => {
    const resolver: MemberResolver = { devicesForAnchor: vi.fn(async () => { throw new Error('boom'); }) };
    const out = await resolveContextBindings(hierarchy('first'), { building: anchorBinding('b1') }, {}, resolver);
    expect(out).toEqual({ building: anchorBinding('b1') });
  });

  it('passes a manifest (base) binding through for an undeclared slot', async () => {
    const d = def({ s1: { type: 'device' } });
    const out = await resolveContextBindings(d, { extra: deviceBinding('x') }, {}, memberResolver({}));
    expect(out.extra).toEqual(deviceBinding('x'));
  });

  it('drops a selection that targets an UNDECLARED slot (no spurious binding/restream)', async () => {
    const d = def({ s1: { type: 'device' } });
    // A mis-authored drill target: a slot that doesn't exist and isn't manifest-bound.
    const out = await resolveContextBindings(d, {}, { ghost: deviceBinding('x') }, memberResolver({}));
    expect('ghost' in out).toBe(false);
  });

  it('ignores a type-incompatible selection on a root slot (keeps the prior binding)', async () => {
    // A device drill mistakenly targets the anchor-typed `building` root slot: the pick is
    // "not applicable" and must NOT re-bind the anchor to a device (which would collapse
    // the whole context). The default anchor binding stands.
    const d = def({ building: { type: 'anchor', defaultBinding: anchorBinding('b1') } });
    const out = await resolveContextBindings(d, { building: anchorBinding('b1') }, { building: deviceBinding('t1') }, memberResolver({}));
    expect(out.building).toEqual(anchorBinding('b1'));
  });

  it('is prototype-safe: a real __proto__ slot/selection key never hits the prototype setter', async () => {
    // JSON.parse creates a genuine OWN "__proto__" key (a literal { __proto__: … } would
    // set the prototype instead), so this exercises the setBinding write guard — a naive
    // out['__proto__'] = b would swap out's prototype and drop the binding.
    const slots = JSON.parse('{"__proto__":{"type":"device","defaultBinding":{"kind":"device","deviceToken":"d1"}}}');
    const overlay = JSON.parse('{"__proto__":{"kind":"device","deviceToken":"evil"}}');
    const out = await resolveContextBindings(def(slots), {}, overlay, memberResolver({}));
    expect(Object.getPrototypeOf(out)).toBe(Object.prototype); // prototype NOT swapped
    expect(Object.prototype.hasOwnProperty.call(out, '__proto__')).toBe(false); // binding dropped, not set
  });
});
