// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Towerpulse annex board, as the annex owner (a member limited to ONE customer) opens it.
//
// The board is anchored on AREAS: a Building picker, a Floor picker and a Zone picker scoped under
// the building. A customer user is answered the areas their own devices are in and the devices
// anchored there, so the pickers list the annex alone. This drives the REAL resolver, lister,
// context cascade and candidate resolver over the COMMITTED fixture (the document the sim
// publishes), against a transport that answers as the platform does for that member and, as the
// positive control, for the operator, who sees the tower too.
//
// The negative controls: an operator sees the tower's areas in the same pickers (so the annex-only
// result is the platform's scoping, not an emptiness of the fixture), and a deep link naming a tower
// floor falls back to an annex floor and binds no tower device.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { GraphQLRequestError } from '@devicechain/client';

import { resolveSlotCandidates } from './candidates';
import { createEntityLister } from './entity-lister';
import { ANCHOR_DEVICES, LIST_AREAS } from './queries';
import { createDeviceResolver } from './resolver';
import { resolveViewerBindings } from './viewer-bindings';
import type { DashboardDefinition, SlotBinding } from './types';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args), subscribe: () => () => {} };
});

const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'sim-dashboards', 'tp-annex-dashboard.json');
const load = (): DashboardDefinition => JSON.parse(readFileSync(FIXTURE, 'utf8')) as DashboardDefinition;

// What a scoped caller is answered when it asks for a relationship type.
const scopeUnsupported = () =>
  new GraphQLRequestError('GraphQL error', 200, [
    { message: 'not available', extensions: { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' } },
  ]);

// The campus. The plant has its own type, so a Floor picker limited to tp-floor never offers it.
const AREAS = [
  { token: 'tp-tower', name: 'Tower', type: 'tp-building', annex: false, devices: ['tp-vav-tower-f4-north', 'tp-chiller-tower'] },
  { token: 'tp-tower-f4', name: 'Tower floor 4', type: 'tp-floor', annex: false, devices: ['tp-vav-tower-f4-north'] },
  { token: 'tp-tower-plant', name: 'Tower plant', type: 'tp-plant', annex: false, devices: ['tp-chiller-tower'] },
  { token: 'tp-annex', name: 'Annex', type: 'tp-building', annex: true, devices: ['tp-vav-annex-f1-north', 'tp-vav-annex-f2-north', 'tp-chiller-annex'] },
  { token: 'tp-annex-f1', name: 'Annex floor 1', type: 'tp-floor', annex: true, devices: ['tp-vav-annex-f1-north'] },
  { token: 'tp-annex-f2', name: 'Annex floor 2', type: 'tp-floor', annex: true, devices: ['tp-vav-annex-f2-north'] },
  { token: 'tp-annex-plant', name: 'Annex plant', type: 'tp-plant', annex: true, devices: ['tp-chiller-annex'] },
];
const isAnnexDevice = (d: string) => d.includes('annex');

function platform(scoped: boolean) {
  h.gql.mockImplementation(async (_area: string, doc: unknown, vars: any) => {
    if (doc === LIST_AREAS) {
      const type = vars.criteria.areaTypeToken as string | undefined;
      const results = AREAS.filter((a) => !type || a.type === type)
        .filter((a) => !scoped || a.annex)
        .map((a) => ({ token: a.token, name: a.name }));
      return { areas: { results } };
    }
    if (doc === ANCHOR_DEVICES) {
      const c = vars.criteria;
      if (scoped && c.relationshipType) throw scopeUnsupported();
      const rows = (AREAS.find((a) => a.token === c.target)?.devices ?? []).filter((d) => !scoped || isAnnexDevice(d));
      return {
        anchorDevices: {
          results: rows.map((token) => ({ token, name: token, deviceType: { token: 'vav' } })),
          pagination: { totalRecords: rows.length },
        },
      };
    }
    throw new Error('unexpected query ');
  });
}

const defaults = (def: DashboardDefinition): Record<string, SlotBinding> =>
  Object.fromEntries(Object.entries(def.slots ?? {}).flatMap(([n, s]) => (s.defaultBinding ? [[n, s.defaultBinding]] : [])));
const token = (b: SlotBinding | undefined) =>
  b?.kind === 'anchor' ? b.anchor.targetToken : b?.kind === 'device' ? b.deviceToken : undefined;
const labels = async (def: DashboardDefinition, slot: string, bindings: Record<string, SlotBinding>) =>
  (await resolveSlotCandidates(def, slot, bindings, createDeviceResolver(), createEntityLister())).map((o) => o.label);

beforeEach(() => {
  h.gql.mockReset();
});

describe('the annex board for the annex owner', () => {
  it('declares typed Building and Floor pickers and a Zone under the building, with no customer root', () => {
    const slots = load().slots!;
    expect(slots.building.candidates).toEqual({ areaType: 'tp-building' });
    expect(slots.floor.candidates).toEqual({ areaType: 'tp-floor' });
    expect(slots.selectedZone.scope).toEqual({ parent: 'building', strategy: 'manual' });
    for (const s of Object.values(slots)) {
      if (s.defaultBinding?.kind === 'anchor') expect(s.defaultBinding.anchor.targetType).toBe('area');
    }
  });

  it('keeps its context and lists ONLY annex areas and devices in the pickers', async () => {
    platform(true);
    const def = load();
    const effective = await resolveViewerBindings(def, defaults(def), {}, createDeviceResolver(), createEntityLister());
    expect(token(effective.building)).toBe('tp-annex');
    expect(token(effective.floor)).toBe('tp-annex-f1');
    expect(token(effective.selectedZone)).toBe('tp-vav-annex-f1-north');
    expect(await labels(def, 'building', effective)).toEqual(['Annex']);
    expect(await labels(def, 'floor', effective)).toEqual(['Annex floor 1', 'Annex floor 2']);
    expect(await labels(def, 'selectedZone', effective)).toEqual([
      'tp-chiller-annex',
      'tp-vav-annex-f1-north',
      'tp-vav-annex-f2-north',
    ]);
  });

  it('NEGATIVE CONTROL: an unscoped operator sees the tower in the same pickers', async () => {
    platform(false);
    const def = load();
    const effective = await resolveViewerBindings(def, defaults(def), {}, createDeviceResolver(), createEntityLister());
    expect(await labels(def, 'building', effective)).toEqual(['Tower', 'Annex']);
    expect(await labels(def, 'floor', effective)).toEqual(['Tower floor 4', 'Annex floor 1', 'Annex floor 2']);
  });

  it('a deep link naming a tower floor falls back to an annex floor and binds no tower device', async () => {
    platform(true);
    const def = load();
    const link: Record<string, SlotBinding> = {
      floor: { kind: 'anchor', anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'tp-tower-f4' } },
    };
    const effective = await resolveViewerBindings(def, defaults(def), link, createDeviceResolver(), createEntityLister());
    expect(token(effective.floor)).toBe('tp-annex-f1');
    for (const b of Object.values(effective)) expect(token(b) ?? '').not.toMatch(/tower/);
  });
});
