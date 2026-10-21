// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet read as the live hub performs it: the roster of an anchor, every member's live samples
// kept apart by device, and one bounded, shared seed read per member. These pin the three things a
// fleet table is wrong without -- the device stays beside the value; a fleet bigger than the page
// read says so; and a fleet of N is N reads, not a storm.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver } from './hub';
import { MEMBER_SEED_CONCURRENCY, type MemberBucketCriteria, type MemberRoster, type MemberSeed, type MemberSeedPlan } from './members';
import { GraphQLRequestError } from '@devicechain/client';

import { BUCKETED_MEASUREMENTS, CUSTOMER_DEVICES, DECLARED_METRICS, DEVICE_NAMES, DEVICES_FOR_ANCHOR } from './queries';
import { ANCHOR_PAGE_SIZE, createDeviceResolver } from './resolver';
import type { DatasourceSelector, MeasurementSample } from './types';

const h = vi.hoisted(() => ({
  gql: vi.fn(),
  // The live sinks the hub opened, by device token.
  streams: new Map<string, { next: (d: unknown) => void; error?: (e: unknown) => void }>(),
  opened: 0,
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    gql: (...args: unknown[]) => h.gql(...args),
    isForbiddenError: (e: unknown) => (e as { forbidden?: boolean })?.forbidden === true,
    subscribe: (_a: string, _d: unknown, vars: { deviceToken: string }, sink: { next: (d: unknown) => void }) => {
      h.opened += 1;
      h.streams.set(vars.deviceToken, sink);
      return () => h.streams.delete(vars.deviceToken);
    },
  };
});

const flush = () => new Promise((r) => setTimeout(r, 0));
const WINDOW = { startTime: '2026-10-14T11:00:00.000Z', endTime: '2026-10-14T11:59:59.999Z', intervalSeconds: 60 };
const ANCHOR: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' },
  measurements: ['temperature'],
};

const tokens = (n: number) => Array.from({ length: n }, (_, i) => `d-${String(i + 1).padStart(3, '0')}`);

function sample(deviceToken: string, name: string, value: number, at = '2026-10-14T11:59:00Z'): MeasurementSample {
  return { id: `${deviceToken}-${name}-${at}`, deviceToken, eventType: 0, occurredTime: at, name, value, classifier: null };
}

// A backend with `n` devices in the anchor (and `total` rows in all, when that is more).
function backend(n: number, total = n, failFor?: (token: string) => unknown) {
  h.gql.mockImplementation(async (_area: string, doc: unknown, vars: any) => {
    if (doc === DEVICES_FOR_ANCHOR) {
      return {
        entityRelationships: {
          results: tokens(n).map((token) => ({ source: { token } })),
          pagination: { totalRecords: total },
        },
      };
    }
    if (doc === DEVICE_NAMES) {
      return { devicesByToken: (vars.tokens as string[]).map((token) => ({ token, name: `Machine ${token.slice(2)}`, deviceType: { token: 'pump' } })) };
    }
    if (doc === BUCKETED_MEASUREMENTS) {
      const err = failFor?.(vars.criteria.deviceToken);
      if (err) throw err;
      return {
        bucketedMeasurements: [
          { bucketStart: '2026-10-14T11:58:00Z', name: 'temperature', avg: Number(vars.criteria.deviceToken.slice(2)), count: 3 },
        ],
      };
    }
    throw new Error('unexpected query');
  });
}

function newHub(): DashboardHub {
  return new DashboardHub({ resolver: createDeviceResolver() });
}

const countOf = (doc: unknown) => h.gql.mock.calls.filter((c) => c[1] === doc).length;

beforeEach(() => {
  h.gql.mockReset();
  h.streams.clear();
  h.opened = 0;
});

describe('the roster', () => {
  it('lists the anchor members with their names, from one relationship page and ONE batched name read', async () => {
    backend(40);
    const hub = newHub();
    const rosters: MemberRoster[] = [];
    hub.subscribeMembers(ANCHOR, { roster: (r) => rosters.push(r), next: () => {} });
    await flush();
    expect(rosters).toHaveLength(1);
    expect(rosters[0].members).toHaveLength(40);
    expect(rosters[0].members[0]).toEqual({ token: 'd-001', name: 'Machine 001', deviceType: 'pump' });
    expect(rosters[0].unbound).toBe(false);
    expect(countOf(DEVICES_FOR_ANCHOR)).toBe(1);
    expect(countOf(DEVICE_NAMES)).toBe(1);
  });

  it('reports the true size of an anchor larger than the page it reads', async () => {
    backend(ANCHOR_PAGE_SIZE, 812);
    const hub = newHub();
    let roster: MemberRoster | undefined;
    hub.subscribeMembers(ANCHOR, { roster: (r) => (roster = r), next: () => {} });
    await flush();
    expect(roster!.members).toHaveLength(ANCHOR_PAGE_SIZE);
    expect(roster!.total).toBe(812);
    // The page it asked for is the cap the widget will say it was cut at.
    const asked = h.gql.mock.calls.find((c) => c[1] === DEVICES_FOR_ANCHOR)![2].criteria;
    expect(asked.pageSize).toBe(ANCHOR_PAGE_SIZE);
  });

  it('still lists the members when the name read fails: labelled by token, not lost', async () => {
    backend(3);
    const inner = h.gql.getMockImplementation()!;
    h.gql.mockImplementation(async (a: string, doc: unknown, v: unknown) => {
      if (doc === DEVICE_NAMES) throw new Error('names are down');
      return inner(a, doc, v);
    });
    let roster: MemberRoster | undefined;
    newHub().subscribeMembers(ANCHOR, { roster: (r) => (roster = r), next: () => {} });
    await flush();
    expect(roster!.members.map((m) => m.name)).toEqual([null, null, null]);
  });

  it('resolves an unbound slot to an empty, flagged roster without a single round trip', async () => {
    backend(5);
    let roster: MemberRoster | undefined;
    newHub().subscribeMembers({ kind: 'unbound', measurements: ['temperature'] }, { roster: (r) => (roster = r), next: () => {} });
    await flush();
    expect(roster).toEqual({ members: [], total: 0, unbound: true });
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('tells a refusal from a failure', async () => {
    h.gql.mockRejectedValue({ forbidden: true });
    const forbidden = vi.fn();
    const error = vi.fn();
    newHub().subscribeMembers(ANCHOR, { roster: () => {}, next: () => {}, forbidden, error });
    await flush();
    expect(forbidden).toHaveBeenCalledTimes(1);
    expect(error).not.toHaveBeenCalled();
    h.gql.mockRejectedValue(new Error('boom'));
    const error2 = vi.fn();
    newHub().subscribeMembers(ANCHOR, { roster: () => {}, next: () => {}, forbidden, error: error2 });
    await flush();
    expect(error2).toHaveBeenCalledTimes(1);
  });
});

describe('the live samples', () => {
  it('keeps two devices that report the SAME measurement name apart: each sample arrives with its own device', async () => {
    backend(2);
    const hub = newHub();
    const got: MeasurementSample[] = [];
    hub.subscribeMembers(ANCHOR, { roster: () => {}, next: (s) => got.push(s) });
    await flush();
    h.streams.get('d-001')!.next({ measurementStream: sample('d-001', 'temperature', 20) });
    h.streams.get('d-002')!.next({ measurementStream: sample('d-002', 'temperature', 90) });
    expect(got.map((s) => [s.deviceToken, s.name, s.value])).toEqual([
      ['d-001', 'temperature', 20],
      ['d-002', 'temperature', 90],
    ]);
  });

  it('opens one upstream stream per member, shared by every widget on the same anchor, and closes them on dispose', async () => {
    backend(12);
    const hub = newHub();
    const a = hub.subscribeMembers(ANCHOR, { roster: () => {}, next: () => {} });
    const b = hub.subscribeMembers(ANCHOR, { roster: () => {}, next: () => {} });
    await flush();
    expect(hub.openStreamCount).toBe(12);
    expect(h.opened).toBe(12);
    a();
    expect(hub.openStreamCount).toBe(12);
    b();
    expect(hub.openStreamCount).toBe(0);
  });

  it('attaches nothing when disposed before the roster arrives', async () => {
    backend(5);
    const hub = newHub();
    const got = vi.fn();
    const dispose = hub.subscribeMembers(ANCHOR, { roster: got, next: () => {} });
    dispose();
    await flush();
    expect(got).not.toHaveBeenCalled();
    expect(hub.openStreamCount).toBe(0);
  });
});

// A plan of one read over the window: what each device's seed is made of is the widget's business
// (fleet-seed.ts in the widgets package); the hub's is to bound, share and settle the reads.
function planOf(...windows: MemberBucketCriteria[]): MemberSeedPlan {
  return {
    async seed(deviceToken, read) {
      const latest: MemberSeed['latest'] = {};
      for (const w of windows) {
        for (const b of await read(w)) latest[b.name] = sample(deviceToken, b.name, b.avg ?? Number.NaN, b.bucketStart);
      }
      return { latest, observed: Object.keys(latest) };
    },
  };
}
const PLAN = planOf(WINDOW);

describe('the seed: no query storm', () => {
  it('costs the plan’s reads per member plus two for the whole roster (N+2 for a one-read plan)', async () => {
    backend(60);
    const hub = newHub();
    hub.subscribeMembers(ANCHOR, { roster: () => {}, next: () => {} });
    await flush();
    await hub.readMemberSeeds(tokens(60), PLAN, () => {});
    expect(countOf(DEVICES_FOR_ANCHOR)).toBe(1);
    expect(countOf(DEVICE_NAMES)).toBe(1);
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(60);
    expect(h.gql).toHaveBeenCalledTimes(62);
  });

  it('asks the event store exactly what the plan asked, for the device it is reading', async () => {
    backend(1);
    await newHub().readMemberSeeds(['d-001'], planOf({ ...WINDOW, name: 'temperature' }), () => {});
    const call = h.gql.mock.calls.find((c) => c[1] === BUCKETED_MEASUREMENTS)!;
    expect(call[0]).toBe('event-management');
    expect(call[2]).toEqual({ criteria: { deviceToken: 'd-001', ...WINDOW, name: 'temperature' } });
  });

  it('never has more than the limit in flight, whatever the size of the fleet or the plan', async () => {
    let active = 0;
    let peak = 0;
    h.gql.mockImplementation(async () => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise((r) => setTimeout(r, 1));
      active -= 1;
      return { bucketedMeasurements: [] };
    });
    await newHub().readMemberSeeds(tokens(100), planOf(WINDOW, { ...WINDOW, name: 'x' }), () => {});
    expect(peak).toBe(MEMBER_SEED_CONCURRENCY);
    expect(h.gql).toHaveBeenCalledTimes(200);
  });

  it('makes two widgets that ask the same question share the reads', async () => {
    backend(30);
    const hub = newHub();
    const seen: string[] = [];
    await Promise.all([
      hub.readMemberSeeds(tokens(30), PLAN, (t) => seen.push(`a${t}`)),
      hub.readMemberSeeds(tokens(30), planOf(WINDOW), (t) => seen.push(`b${t}`)),
    ]);
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(30);
    expect(seen).toHaveLength(60); // both widgets got every device
  });

  it('asks again for a different window or name (a different question)', async () => {
    backend(4);
    const hub = newHub();
    await hub.readMemberSeeds(tokens(4), PLAN, () => {});
    await hub.readMemberSeeds(tokens(4), planOf({ ...WINDOW, startTime: '2026-10-14T10:00:00.000Z' }), () => {});
    await hub.readMemberSeeds(tokens(4), planOf({ ...WINDOW, name: 'temperature' }), () => {});
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(12);
  });

  it('never shares a read between two different questions in flight at once', async () => {
    backend(4);
    const hub = newHub();
    await Promise.all([
      hub.readMemberSeeds(tokens(4), PLAN, () => {}),
      hub.readMemberSeeds(tokens(4), planOf({ ...WINDOW, name: 'temperature' }), () => {}),
      hub.readMemberSeeds(tokens(4), planOf({ ...WINDOW, startTime: '2026-10-14T10:00:00.000Z' }), () => {}),
    ]);
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(12);
  });

  it('delivers each device seed under its own token', async () => {
    backend(3);
    const got: Array<[string, number | undefined]> = [];
    await newHub().readMemberSeeds(tokens(3), PLAN, (t, seed) => got.push([t, seed?.latest.temperature?.value ?? undefined]));
    expect(got.sort()).toEqual([
      ['d-001', 1],
      ['d-002', 2],
      ['d-003', 3],
    ]);
  });

  it('reports a device whose read failed as failed, and lets every other device through', async () => {
    backend(3, 3, (t) => (t === 'd-002' ? new Error('down') : undefined));
    const got = new Map<string, unknown>();
    const why = new Map<string, unknown>();
    await newHub().readMemberSeeds(tokens(3), PLAN, (t, seed, failure) => {
      got.set(t, seed);
      why.set(t, failure);
    });
    expect(got.get('d-002')).toBeNull();
    expect(why.get('d-002')).toBe('failed');
    expect(got.get('d-001')).not.toBeNull();
    expect(got.get('d-003')).not.toBeNull();
  });

  it('classifies a refusal as forbidden, not as a failure', async () => {
    backend(1, 1, () => ({ forbidden: true }));
    const why: unknown[] = [];
    await newHub().readMemberSeeds(['d-001'], PLAN, (_t, _s, failure) => why.push(failure));
    expect(why).toEqual(['forbidden']);
  });

  it('does not start reads for a widget that has gone away', async () => {
    let cancelled = false;
    h.gql.mockImplementation(async () => {
      cancelled = true; // the widget unmounts while the first reads are in flight
      await new Promise((r) => setTimeout(r, 1));
      return { bucketedMeasurements: [] };
    });
    const got: string[] = [];
    await newHub().readMemberSeeds(tokens(50), PLAN, (t) => got.push(t), () => cancelled);
    expect(h.gql.mock.calls.length).toBeLessThanOrEqual(MEMBER_SEED_CONCURRENCY);
    expect(got).toHaveLength(0);
  });

  it('re-asks, for a widget still here, a shared read that its first asker abandoned before it started', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    h.gql.mockImplementation(async (_area: string, _doc: unknown, vars: any) => {
      if (vars.criteria.deviceToken !== 'late') await gate;
      return { bucketedMeasurements: [{ bucketStart: '2026-10-14T11:58:00Z', name: 'temperature', avg: 7, count: 1 }] };
    });
    const hub = newHub();
    // Six devices fill the limiter, so the next read waits its turn.
    const busy = hub.readMemberSeeds(tokens(MEMBER_SEED_CONCURRENCY), PLAN, () => {});
    let goneA = false;
    const gotA: string[] = [];
    const gotB: string[] = [];
    const a = hub.readMemberSeeds(['late'], PLAN, (t) => gotA.push(t), () => goneA);
    const b = hub.readMemberSeeds(['late'], PLAN, (t) => gotB.push(t));
    goneA = true; // A goes away while the read it started (and B shares) is still queued
    release();
    await Promise.all([a, b, busy]);
    expect(gotA).toEqual([]);
    expect(gotB).toEqual(['late']);
    expect(h.gql.mock.calls.filter((c) => c[2].criteria.deviceToken === 'late')).toHaveLength(1);
  });
});

describe('a resolver without member support', () => {
  it('still yields a roster from the tokens, with no names and the page as the fleet', async () => {
    const resolver: DeviceResolver = { devicesForAnchor: vi.fn(async () => ['a', 'b']), deviceExists: vi.fn(async () => true) };
    let roster: MemberRoster | undefined;
    new DashboardHub({ resolver }).subscribeMembers(ANCHOR, { roster: (r) => (roster = r), next: () => {} });
    await flush();
    expect(roster).toEqual({ members: [{ token: 'a', name: null }, { token: 'b', name: null }], total: 2, unbound: false });
  });
});

// A customer anchor reads customerDevices -- the read a customer user is answered on -- and every
// other anchor reads the relationship graph, whoever is viewing.
describe('the read each anchor kind takes', () => {
  const CUSTOMER: DatasourceSelector = {
    kind: 'anchor',
    anchor: { relationship: 'assigned', targetType: 'customer', targetToken: 'acme' },
    measurements: ['temperature'],
  };

  it('lists a customer anchor through customerDevices, names and size included, with no second read', async () => {
    h.gql.mockImplementation(async (_area: string, doc: unknown) => {
      if (doc === CUSTOMER_DEVICES) {
        return {
          customerDevices: {
            results: [
              { token: 'd-001', name: 'Pump one', deviceType: { token: 'pump' } },
              { token: 'd-002', name: null, deviceType: null },
            ],
            pagination: { totalRecords: 7 },
          },
        };
      }
      throw new Error('unexpected query');
    });
    let roster: MemberRoster | undefined;
    newHub().subscribeMembers(CUSTOMER, { roster: (r) => (roster = r), next: () => {} });
    await flush();
    expect(roster!.members).toEqual([
      { token: 'd-001', name: 'Pump one', deviceType: 'pump' },
      { token: 'd-002', name: null, deviceType: null },
    ]);
    expect(roster!.total).toBe(7);
    const call = h.gql.mock.calls.find((c) => c[1] === CUSTOMER_DEVICES)!;
    expect(call[0]).toBe('device-management');
    expect(call[2]).toEqual({
      criteria: { pageNumber: 1, pageSize: ANCHOR_PAGE_SIZE, customer: 'acme', relationshipType: 'assigned' },
    });
    expect(countOf(DEVICES_FOR_ANCHOR)).toBe(0);
    expect(countOf(DEVICE_NAMES)).toBe(0);
  });

  it('lists an asset or area anchor through the relationship graph', async () => {
    backend(2);
    for (const targetType of ['area', 'asset'] as const) {
      const ds: DatasourceSelector = { ...ANCHOR, anchor: { ...ANCHOR.anchor, targetType } } as DatasourceSelector;
      newHub().subscribeMembers(ds, { roster: () => {}, next: () => {} });
    }
    await flush();
    expect(countOf(DEVICES_FOR_ANCHOR)).toBe(2);
    expect(countOf(CUSTOMER_DEVICES)).toBe(0);
  });
});

// What a device type's ACTIVE profile declares: read once per type, for any number of devices and
// widgets, and never able to take a roster down. A viewer limited to some customers makes the same
// read (device types and profile versions are catalogue reads open to any member), and if it is
// refused anyway the answer is "unknown", which the fleet widgets read as declared.
describe('declaredMetrics', () => {
  const declares = (types: Record<string, Array<{ metricKey: string }> | null | 'no-profile'>) =>
    h.gql.mockImplementation(async (_area: string, doc: unknown, vars: any) => {
      if (doc !== DECLARED_METRICS) throw new Error('unexpected query');
      return {
        deviceTypesByToken: (vars.tokens as string[])
          .filter((t) => t in types)
          .map((token) => {
            const m = types[token];
            return { token, profile: m === 'no-profile' ? null : { activeProfileVersion: m ? { metrics: m } : null } };
          }),
      };
    });

  it('reads the metric names of the type active profile version, calculated ones included', async () => {
    declares({ tank: [{ metricKey: 'fuel' }, { metricKey: 'fuel_pct' }] });
    expect(await newHub().declaredMetrics('tank')).toEqual(['fuel', 'fuel_pct']);
    const call = h.gql.mock.calls[0];
    expect(call[0]).toBe('device-management');
    expect(call[2]).toEqual({ tokens: ['tank'] });
  });

  it('is null where the type has nothing published to say: no profile, never published, or not found', async () => {
    declares({ bare: 'no-profile', draft: null });
    const hub = newHub();
    expect(await hub.declaredMetrics('bare')).toBeNull();
    expect(await hub.declaredMetrics('draft')).toBeNull();
    expect(await hub.declaredMetrics('missing')).toBeNull();
  });

  it('asks once per distinct type, however many devices and widgets want it', async () => {
    declares({ tank: [{ metricKey: 'fuel' }], crusher: [{ metricKey: 'throughput' }] });
    const hub = newHub();
    const asked = await Promise.all(['tank', 'crusher', 'tank', 'tank', 'crusher'].map((t) => hub.declaredMetrics(t)));
    await hub.declaredMetrics('tank');
    expect(asked[0]).toEqual(['fuel']);
    expect(asked[1]).toEqual(['throughput']);
    // Both types asked in one tick go in ONE read, and each is asked once.
    expect(countOf(DECLARED_METRICS)).toBe(1);
    expect(h.gql.mock.calls[0][2]).toEqual({ tokens: ['tank', 'crusher'] });
  });

  it('reads a board of many types in one request per hundred types, not one per type', async () => {
    const many = Object.fromEntries(Array.from({ length: 150 }, (_, i) => [`t${i}`, [{ metricKey: `m${i}` }]]));
    declares(many);
    const hub = newHub();
    const asked = await Promise.all(Object.keys(many).map((t) => hub.declaredMetrics(t)));
    expect(asked[149]).toEqual(['m149']);
    expect(countOf(DECLARED_METRICS)).toBe(2);
  });

  it('is null for a published version that declares no metric: an undeclared measurement is still accepted', async () => {
    declares({ open: [] });
    expect(await newHub().declaredMetrics('open')).toBeNull();
  });

  it('is null when the read fails, not a rejection, and asks again next time rather than keeping the failure', async () => {
    h.gql.mockRejectedValueOnce(new Error('profile read failed'));
    declares({ tank: [{ metricKey: 'fuel' }] });
    const hub = newHub();
    expect(await hub.declaredMetrics('tank')).toBeNull();
    expect(await hub.declaredMetrics('tank')).toEqual(['fuel']);
  });

  it('is null for a viewer the read is refused to: unknown, never an exclusion', async () => {
    h.gql.mockRejectedValue({ forbidden: true });
    expect(await newHub().declaredMetrics('tank')).toBeNull();
  });

  it('is null from a resolver that cannot look a type up', async () => {
    const hub = new DashboardHub({ resolver: { devicesForAnchor: async () => [], deviceExists: async () => true } });
    expect(await hub.declaredMetrics('tank')).toBeNull();
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('reads a customer-scoped roster and the profiles of its types through the same two reads', async () => {
    h.gql.mockImplementation(async (_area: string, doc: unknown, vars: any) => {
      if (doc === CUSTOMER_DEVICES) {
        return { customerDevices: { results: [{ token: 'd-001', name: 'Tank', deviceType: { token: 'tank' } }], pagination: { totalRecords: 1 } } };
      }
      if (doc === DECLARED_METRICS) {
        return { deviceTypesByToken: [{ token: vars.tokens[0], profile: { activeProfileVersion: { metrics: [{ metricKey: 'fuel' }] } } }] };
      }
      throw new Error('unexpected query');
    });
    const hub = newHub();
    let roster: MemberRoster | undefined;
    hub.subscribeMembers(
      { kind: 'anchor', anchor: { relationship: 'assigned', targetType: 'customer', targetToken: 'acme' }, measurements: ['fuel'] },
      { roster: (r) => (roster = r), next: () => {} },
    );
    await flush();
    expect(roster!.members[0].deviceType).toBe('tank');
    expect(await hub.declaredMetrics(roster!.members[0].deviceType!)).toEqual(['fuel']);
    expect(countOf(DEVICE_NAMES)).toBe(0);
  });
});

describe('isDatasourceSupported', () => {
  const scopeUnsupported = () =>
    new GraphQLRequestError('GraphQL error', 200, [
      { message: 'not available', extensions: { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' } },
    ]);

  it('is false only for an anchor whose resolution is refused as not offered to the viewer', async () => {
    h.gql.mockRejectedValue(scopeUnsupported());
    expect(await newHub().isDatasourceSupported(ANCHOR)).toBe(false);
  });

  it('is true for an ordinary refusal, a failure, a resolved anchor and anything that is not an anchor', async () => {
    h.gql.mockRejectedValue(new GraphQLRequestError('GraphQL error', 200, [{ message: 'forbidden', extensions: { code: 'FORBIDDEN' } }]));
    expect(await newHub().isDatasourceSupported(ANCHOR)).toBe(true);
    h.gql.mockRejectedValue(new Error('boom'));
    expect(await newHub().isDatasourceSupported(ANCHOR)).toBe(true);
    backend(3);
    expect(await newHub().isDatasourceSupported(ANCHOR)).toBe(true);
    h.gql.mockRejectedValue(scopeUnsupported());
    const hub = newHub();
    expect(await hub.isDatasourceSupported({ kind: 'device', deviceToken: 'd-001', measurements: [] })).toBe(true);
    expect(await hub.isDatasourceSupported(undefined)).toBe(true);
  });

  it('shares the resolution the widget channel makes: a supported anchor is read once', async () => {
    backend(4);
    const hub = newHub();
    expect(await hub.isDatasourceSupported(ANCHOR)).toBe(true);
    hub.subscribeWidget(ANCHOR, { next: () => {} });
    await flush();
    expect(countOf(DEVICES_FOR_ANCHOR)).toBe(1);
  });
});
