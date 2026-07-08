// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { resolveSlotCandidates } from './candidates';
import type { MemberResolver } from './context';
import type {
  DashboardDefinition,
  EntityCandidateLister,
  SlotBinding,
  SlotDefinition,
} from './types';

const def = (slots: Record<string, SlotDefinition>): DashboardDefinition => ({
  schemaVersion: 1,
  title: '',
  canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [],
  slots,
});

// A resolver whose anchor membership is fixed (returned unsorted to prove sorting).
const resolverOf = (members: string[]): MemberResolver => ({
  devicesForAnchor: async () => members,
});

const throwingResolver: MemberResolver = {
  devicesForAnchor: async () => {
    throw new Error('membership down');
  },
};

// A lister returning fixed rows per kind.
const listerOf = (rows: Array<{ token: string; name?: string | null }>): EntityCandidateLister =>
  async () => rows;

const throwingLister: EntityCandidateLister = async () => {
  throw new Error('list down');
};

const buildingBinding: SlotBinding = {
  kind: 'anchor',
  anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'bldg-1' },
};

describe('resolveSlotCandidates', () => {
  it('returns a scoped child slot’s parent members, sorted, flagging the current pick', async () => {
    const d = def({
      building: { type: 'anchor' },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'manual' } },
    });
    const bindings = { building: buildingBinding, therm: { kind: 'device', deviceToken: 'b' } as SlotBinding };
    const out = await resolveSlotCandidates(d, 'therm', bindings, resolverOf(['c', 'a', 'b']), listerOf([]));
    expect(out.map((c) => c.label)).toEqual(['a', 'b', 'c']);
    expect(out.map((c) => c.selected)).toEqual([false, true, false]);
    expect(out[0].binding).toEqual({ kind: 'device', deviceToken: 'a' });
  });

  it('returns no candidates for a scoped child whose parent is unbound', async () => {
    const d = def({
      building: { type: 'anchor' },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'manual' } },
    });
    const out = await resolveSlotCandidates(d, 'therm', {}, resolverOf(['a']), listerOf([]));
    expect(out).toEqual([]);
  });

  it('offers no options for a first-strategy scoped slot (fully auto-derived, not pickable)', async () => {
    const d = def({
      building: { type: 'anchor' },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'first' } },
    });
    // Even with a bound parent + real members, a 'first' slot yields nothing to pick.
    const out = await resolveSlotCandidates(d, 'therm', { building: buildingBinding }, resolverOf(['a', 'b']), listerOf([]));
    expect(out).toEqual([]);
  });

  it('fails safe to no candidates on a membership error (manual slot)', async () => {
    const d = def({
      building: { type: 'anchor' },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'manual' } },
    });
    const bindings = { building: buildingBinding };
    const out = await resolveSlotCandidates(d, 'therm', bindings, throwingResolver, listerOf([]));
    expect(out).toEqual([]);
  });

  it('lists tenant areas for a root anchor slot, reusing its binding as the relationship template', async () => {
    const d = def({ building: { type: 'anchor', defaultBinding: buildingBinding } });
    const rows = [
      { token: 'bldg-1', name: 'HQ' },
      { token: 'bldg-2', name: 'Annex' },
    ];
    const out = await resolveSlotCandidates(d, 'building', { building: buildingBinding }, resolverOf([]), listerOf(rows));
    expect(out.map((c) => c.label)).toEqual(['HQ', 'Annex']);
    expect(out[0].binding).toEqual({
      kind: 'anchor',
      anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'bldg-1' },
    });
    expect(out[0].selected).toBe(true); // matches the current building
    expect(out[1].selected).toBe(false);
  });

  it('returns no candidates for a root anchor slot with no binding template', async () => {
    const d = def({ building: { type: 'anchor' } }); // no default, none in bindings
    const out = await resolveSlotCandidates(d, 'building', {}, resolverOf([]), listerOf([{ token: 'x' }]));
    expect(out).toEqual([]);
  });

  it('lists tenant devices for a root device slot', async () => {
    const d = def({ dev: { type: 'device' } });
    const out = await resolveSlotCandidates(
      d,
      'dev',
      { dev: { kind: 'device', deviceToken: 'd2' } },
      resolverOf([]),
      listerOf([{ token: 'd1' }, { token: 'd2', name: 'Sensor 2' }]),
    );
    expect(out.map((c) => c.label)).toEqual(['d1', 'Sensor 2']);
    expect(out.map((c) => c.selected)).toEqual([false, true]);
  });

  it('fails safe to no candidates when the lister throws', async () => {
    const d = def({ dev: { type: 'device' } });
    const out = await resolveSlotCandidates(d, 'dev', {}, resolverOf([]), throwingLister);
    expect(out).toEqual([]);
  });

  it('returns no candidates for an undeclared slot', async () => {
    const d = def({ dev: { type: 'device' } });
    const out = await resolveSlotCandidates(d, 'nope', {}, resolverOf(['a']), listerOf([{ token: 'x' }]));
    expect(out).toEqual([]);
  });

  it('reads slots via own properties (a __proto__ slot name is absent, not the prototype)', async () => {
    const d = def({ dev: { type: 'device' } });
    const out = await resolveSlotCandidates(d, '__proto__', {}, resolverOf(['a']), listerOf([{ token: 'x' }]));
    expect(out).toEqual([]);
  });
});
