// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Area boards for a member limited to some customers (a customer user), and for an unscoped viewer.
//
// The platform answers a customer user about the areas and devices THEIR devices reach: the areas
// query lists only those areas, anchorDevices narrows an area's members to their devices and refuses
// a relationship type from them, and the relationship graph is closed to them. This drives the REAL
// resolver, the REAL lister, the REAL context cascade and the REAL candidate resolver over a mocked
// transport that answers that way (and, for an unscoped viewer, answers everything).
//
// The negative controls put back what the dashboards did before: members read through the
// relationship graph, and a root picker over a lister that is refused. If those did not fail, the
// harness would be reading something other than the refusal and the passing cases would prove nothing.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { GraphQLRequestError } from '@devicechain/client';

import { resolveSlotCandidates } from './candidates';
import type { MemberResolver } from './context';
import { createEntityLister } from './entity-lister';
import { DashboardHub } from './hub';
import { ANCHOR_DEVICES, LIST_AREAS } from './queries';
import { createDeviceResolver } from './resolver';
import { resolveViewerBindings } from './viewer-bindings';
import type { AnchorTarget, DashboardDefinition, EntityCandidateLister, SlotBinding } from './types';

const h = vi.hoisted(() => ({ gql: vi.fn(), token: null as string | null }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args), resolveAuthToken: async () => h.token, subscribe: () => () => {} };
});

// The tenant: a building and three floors, one of them shared by two customers. Acme sees
// floor-shared, floor-acme and building-acme, never floor-globex.
const AREAS = [
  { token: 'floor-shared', name: 'Shared floor', type: 'tp-floor', devices: ['acme-vav', 'globex-vav'] },
  { token: 'floor-acme', name: 'Acme floor', type: 'tp-floor', devices: ['acme-ahu'] },
  { token: 'building-acme', name: 'Acme building', type: 'tp-building', devices: ['acme-vav', 'acme-ahu'] },
  { token: 'floor-globex', name: 'Globex floor', type: 'tp-floor', devices: ['globex-vav'] },
];
const ACME_DEVICES = new Set(['acme-vav', 'acme-ahu']);

const scopeUnsupported = () =>
  new GraphQLRequestError('GraphQL error', 200, [
    { message: 'not available', extensions: { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' } },
  ]);

const b64 = (o: object) => Buffer.from(JSON.stringify(o)).toString('base64url');
const jwt = (claims: object) => `${b64({ alg: 'none' })}.${b64({ sub: 'u', exp: 4102444800, ...claims })}.sig`;

beforeEach(() => {
  h.token = null;
});

// What the platform answers: `scoped` is Acme's customer user, otherwise the tenant admin.
function platform(scoped: boolean) {
  const calls: Array<{ doc: string; vars: any }> = [];
  h.gql.mockImplementation(async (_area: string, doc: unknown, vars: any) => {
    if (doc === LIST_AREAS) {
      calls.push({ doc: 'areas', vars });
      const type = vars.criteria.areaTypeToken as string | undefined;
      const results = AREAS.filter((a) => !type || a.type === type)
        .filter((a) => !scoped || a.devices.some((d) => ACME_DEVICES.has(d)))
        .map((a) => ({ token: a.token, name: a.name }));
      return { areas: { results } };
    }
    if (doc === ANCHOR_DEVICES) {
      calls.push({ doc: 'anchorDevices', vars });
      const c = vars.criteria;
      if (scoped && c.relationshipType) throw scopeUnsupported();
      const found = AREAS.find((a) => a.token === c.target);
      const rows = (found?.devices ?? []).filter((d) => !scoped || ACME_DEVICES.has(d));
      return {
        anchorDevices: {
          results: rows.map((token) => ({ token, name: token, deviceType: { token: 'vav' } })),
          pagination: { totalRecords: rows.length },
        },
      };
    }
    // The relationship graph: closed to a scoped member, answered to the tenant admin.
    if (typeof doc === 'string' && doc.includes('entityRelationships')) {
      calls.push({ doc: 'entityRelationships', vars });
      if (scoped) throw scopeUnsupported();
      const found = AREAS.find((a) => a.token === vars.criteria.target);
      return { entityRelationships: { results: (found?.devices ?? []).map((token) => ({ source: { token } })) } };
    }
    throw new Error('unexpected query');
  });
  return calls;
}

const anchorOf = (token: string): AnchorTarget => ({ relationship: 'assigned', targetType: 'area', targetToken: token });
const area = (token: string): SlotBinding => ({ kind: 'anchor', anchor: anchorOf(token) });

// A Floor picker over tp-floor areas, and a Zone picked from the floor's members.
function board(defaultFloor: string): DashboardDefinition {
  return {
    schemaVersion: 2,
    title: 'Annex',
    widgets: [],
    slots: {
      floor: { type: 'anchor', defaultBinding: area(defaultFloor), candidates: { areaType: 'tp-floor' } },
      zone: { type: 'device', scope: { parent: 'floor', strategy: 'manual' } },
    },
  } as unknown as DashboardDefinition;
}
const defaults = (def: DashboardDefinition): Record<string, SlotBinding> =>
  Object.fromEntries(Object.entries(def.slots ?? {}).flatMap(([n, s]) => (s.defaultBinding ? [[n, s.defaultBinding]] : [])));
const floorToken = (b: SlotBinding | undefined) => (b?.kind === 'anchor' ? b.anchor.targetToken : undefined);

// The pre-change member read: the relationship graph, which a customer user is refused.
const legacyMembers: MemberResolver = {
  async devicesForAnchor(anchor: AnchorTarget): Promise<string[]> {
    const client: any = await import('@devicechain/client');
    const r: any = await client.gql('device-management', 'query { entityRelationships }', {
      criteria: { target: anchor.targetToken },
    });
    return r.entityRelationships.results.map((x: { source: { token: string } }) => x.source.token);
  },
};
const refusedLister: EntityCandidateLister = async () => {
  throw scopeUnsupported();
};

beforeEach(() => {
  h.gql.mockReset();
});

describe('a customer user opening an area board', () => {
  it('lists only the visible areas of the declared type in the Floor picker', async () => {
    platform(true);
    const def = board('floor-acme');
    const resolver = createDeviceResolver();
    const lister = createEntityLister();
    const effective = await resolveViewerBindings(def, defaults(def), {}, resolver, lister);
    const options = await resolveSlotCandidates(def, 'floor', effective, resolver, lister);
    // floor-globex is not theirs, building-acme is the wrong type.
    expect(options.map((o) => floorToken(o.binding))).toEqual(['floor-shared', 'floor-acme']);
    expect(options.filter((o) => o.selected).map((o) => o.label)).toEqual(['Acme floor']);
  });

  it('offers the Zone only the parent floors members they can see', async () => {
    platform(true);
    const def = board('floor-shared');
    const resolver = createDeviceResolver();
    const lister = createEntityLister();
    const base = { ...defaults(def), zone: { kind: 'device', deviceToken: 'acme-vav' } as SlotBinding };
    const effective = await resolveViewerBindings(def, base, {}, resolver, lister);
    expect(effective.zone).toEqual({ kind: 'device', deviceToken: 'acme-vav' });
    const zones = await resolveSlotCandidates(def, 'zone', effective, resolver, lister);
    // The shared floor holds a Globex device too; it is not named.
    expect(zones.map((o) => o.label)).toEqual(['acme-vav']);
  });

  it('falls back to the first visible floor when the saved floor is not one of theirs, with no error', async () => {
    platform(true);
    const def = board('floor-globex');
    const effective = await resolveViewerBindings(def, defaults(def), {}, createDeviceResolver(), createEntityLister());
    expect(floorToken(effective.floor)).toBe('floor-shared');
    // A deep link naming another customer's floor falls back the same way.
    const own = board('floor-acme');
    const linked = await resolveViewerBindings(own, defaults(own), { floor: area('floor-globex') }, createDeviceResolver(), createEntityLister());
    expect(floorToken(linked.floor)).toBe('floor-shared');
  });

  it('keeps a visible floor, and keeps the binding when there is no lister, it fails, or it lists nothing', async () => {
    platform(true);
    const def = board('floor-acme');
    const kept = await resolveViewerBindings(def, defaults(def), {}, createDeviceResolver(), createEntityLister());
    expect(floorToken(kept.floor)).toBe('floor-acme');
    const gone = board('floor-globex');
    for (const lister of [undefined, refusedLister, async () => []] as Array<EntityCandidateLister | undefined>) {
      const out = await resolveViewerBindings(gone, defaults(gone), {}, createDeviceResolver(), lister);
      expect(floorToken(out.floor)).toBe('floor-globex');
    }
  });

  it('is offered an area-bound widget: the binding resolves and is supported', async () => {
    const calls = platform(true);
    const hub = new DashboardHub({ resolver: createDeviceResolver() });
    expect(await hub.isDatasourceSupported({ kind: 'anchor', anchor: anchorOf('floor-shared'), measurements: ['temperature'] })).toBe(true);
    // The relationship type is sent first, refused for a scoped caller, then retried without it.
    const asked = calls.filter((c) => c.doc === 'anchorDevices').map((c) => c.vars.criteria.relationshipType);
    expect(asked).toEqual(['assigned', undefined]);
    expect(calls.some((c) => c.doc === 'entityRelationships')).toBe(false);
  });

  it('a token carrying a scope sends no relationship type: zero refused anchorDevices calls, still only its own devices', async () => {
    h.token = jwt({ scp: ['customer:acme'] });
    const calls = platform(true);
    const resolver = createDeviceResolver();
    // Three parallel reads, as a board opening makes.
    const lists = await Promise.all(['floor-shared', 'floor-acme', 'building-acme'].map((t) => resolver.devicesForAnchor(anchorOf(t))));
    expect(lists).toEqual([['acme-vav'], ['acme-ahu'], ['acme-vav', 'acme-ahu']]);
    const asked = calls.filter((c) => c.doc === 'anchorDevices');
    expect(asked).toHaveLength(3);
    expect(asked.every((c) => c.vars.criteria.relationshipType === undefined)).toBe(true);
    // The out-of-scope device never appears.
    expect(lists.flat()).not.toContain('globex-vav');
  });

  it('NEGATIVE CONTROL: an unscoped token still sends the relationship type, so the test above reads the token', async () => {
    h.token = jwt({});
    const calls = platform(false);
    await createDeviceResolver().devicesForAnchor(anchorOf('floor-shared'));
    expect(calls.filter((c) => c.doc === 'anchorDevices').every((c) => c.vars.criteria.relationshipType === 'assigned')).toBe(true);
  });

  it('with no token to read, learns from the first refusal and does not ask again', async () => {
    h.token = null;
    const calls = platform(true);
    const resolver = createDeviceResolver();
    await resolver.devicesForAnchor(anchorOf('floor-shared'));
    await resolver.devicesForAnchor(anchorOf('floor-acme'));
    expect(calls.filter((c) => c.doc === 'anchorDevices').map((c) => c.vars.criteria.relationshipType)).toEqual(['assigned', undefined, undefined]);
  });

  it('still reports an anchor the platform declines outright as not offered', async () => {
    h.gql.mockRejectedValue(scopeUnsupported());
    const hub = new DashboardHub({ resolver: createDeviceResolver() });
    expect(await hub.isDatasourceSupported({ kind: 'anchor', anchor: anchorOf('x'), measurements: [] })).toBe(false);
  });

  it('NEGATIVE CONTROL: members read through the relationship graph, and a refused lister, give the empty pickers', async () => {
    platform(true);
    const def = board('floor-acme');
    const base = { ...defaults(def), zone: { kind: 'device', deviceToken: 'acme-ahu' } as SlotBinding };
    const effective = await resolveViewerBindings(def, base, {}, legacyMembers, refusedLister);
    // The Zone cannot be derived and neither picker has an option: what the board did before.
    expect(effective.zone).toBeUndefined();
    expect(await resolveSlotCandidates(def, 'zone', effective, legacyMembers, createEntityLister())).toEqual([]);
    expect(await resolveSlotCandidates(def, 'floor', effective, legacyMembers, refusedLister)).toEqual([]);
    // The same board over the real resolver is not empty, so the control reads the cause.
    const real = await resolveViewerBindings(def, base, {}, createDeviceResolver(), createEntityLister());
    expect(real.zone).toEqual({ kind: 'device', deviceToken: 'acme-ahu' });
  });
});

describe('an unscoped viewer opening the same board', () => {
  it('sees every area of the type and every member, with the relationship type sent once', async () => {
    const calls = platform(false);
    const def = board('floor-shared');
    const resolver = createDeviceResolver();
    const lister = createEntityLister();
    const effective = await resolveViewerBindings(def, defaults(def), {}, resolver, lister);
    expect(floorToken(effective.floor)).toBe('floor-shared');
    const floors = await resolveSlotCandidates(def, 'floor', effective, resolver, lister);
    expect(floors.map((o) => floorToken(o.binding))).toEqual(['floor-shared', 'floor-acme', 'floor-globex']);
    expect(await resolver.devicesForAnchor(anchorOf('floor-shared'))).toEqual(['acme-vav', 'globex-vav']);
    const asked = calls.filter((c) => c.doc === 'anchorDevices');
    expect(asked.length).toBeGreaterThan(0);
    expect(asked.every((c) => c.vars.criteria.relationshipType === 'assigned')).toBe(true);
  });

  it('keeps a saved floor that exists, and passes the type filter to the areas read', async () => {
    const calls = platform(false);
    const def = board('floor-globex');
    const effective = await resolveViewerBindings(def, defaults(def), {}, createDeviceResolver(), createEntityLister());
    expect(floorToken(effective.floor)).toBe('floor-globex');
    expect(calls.find((c) => c.doc === 'areas')!.vars.criteria.areaTypeToken).toBe('tp-floor');
  });
});
