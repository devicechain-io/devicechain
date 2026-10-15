// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet read as the live hub performs it: the roster of an anchor, every member's live samples
// kept apart by device, and one bounded, shared seed read per member. These pin the three things a
// fleet table is wrong without -- the device stays beside the value; a fleet bigger than the page
// read says so; and a fleet of N is N reads, not a storm.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver } from './hub';
import { MEMBER_SEED_CONCURRENCY, type MemberRoster } from './members';
import { BUCKETED_MEASUREMENTS, DEVICE_NAMES, DEVICES_FOR_ANCHOR } from './queries';
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
      return { devicesByToken: (vars.tokens as string[]).map((token) => ({ token, name: `Machine ${token.slice(2)}` })) };
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
    expect(rosters[0].members[0]).toEqual({ token: 'd-001', name: 'Machine 001' });
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

describe('the seed: no query storm', () => {
  it('costs one read per member plus two for the whole roster (N+2, not N per widget)', async () => {
    backend(60);
    const hub = newHub();
    hub.subscribeMembers(ANCHOR, { roster: () => {}, next: () => {} });
    await flush();
    await hub.readMemberSeeds(tokens(60), WINDOW, () => {});
    expect(countOf(DEVICES_FOR_ANCHOR)).toBe(1);
    expect(countOf(DEVICE_NAMES)).toBe(1);
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(60);
    expect(h.gql).toHaveBeenCalledTimes(62);
  });

  it('asks the store for every measurement of a device in one read (no name filter), over the window', async () => {
    backend(1);
    await newHub().readMemberSeeds(['d-001'], WINDOW, () => {});
    const call = h.gql.mock.calls.find((c) => c[1] === BUCKETED_MEASUREMENTS)!;
    expect(call[0]).toBe('event-management');
    expect(call[2]).toEqual({ criteria: { deviceToken: 'd-001', ...WINDOW } });
  });

  it('never has more than the limit in flight, whatever the size of the fleet', async () => {
    let active = 0;
    let peak = 0;
    h.gql.mockImplementation(async () => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise((r) => setTimeout(r, 1));
      active -= 1;
      return { bucketedMeasurements: [] };
    });
    await newHub().readMemberSeeds(tokens(100), WINDOW, () => {});
    expect(peak).toBe(MEMBER_SEED_CONCURRENCY);
    expect(h.gql).toHaveBeenCalledTimes(100);
  });

  it('makes two widgets that ask for the same window share the reads', async () => {
    backend(30);
    const hub = newHub();
    const seen: string[] = [];
    await Promise.all([
      hub.readMemberSeeds(tokens(30), WINDOW, (t) => seen.push(`a${t}`)),
      hub.readMemberSeeds(tokens(30), WINDOW, (t) => seen.push(`b${t}`)),
    ]);
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(30);
    expect(seen).toHaveLength(60); // both widgets got every device
  });

  it('asks again for a different window (a new span is a new question)', async () => {
    backend(4);
    const hub = newHub();
    await hub.readMemberSeeds(tokens(4), WINDOW, () => {});
    await hub.readMemberSeeds(tokens(4), { ...WINDOW, startTime: '2026-10-14T10:00:00.000Z' }, () => {});
    expect(countOf(BUCKETED_MEASUREMENTS)).toBe(8);
  });

  it('delivers each device seed under its own token', async () => {
    backend(3);
    const got: Array<[string, number | undefined]> = [];
    await newHub().readMemberSeeds(tokens(3), WINDOW, (t, seed) => got.push([t, seed?.latest.temperature?.value]));
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
    await newHub().readMemberSeeds(tokens(3), WINDOW, (t, seed, failure) => {
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
    await newHub().readMemberSeeds(['d-001'], WINDOW, (_t, _s, failure) => why.push(failure));
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
    await newHub().readMemberSeeds(tokens(50), WINDOW, (t) => got.push(t), () => cancelled);
    expect(h.gql.mock.calls.length).toBeLessThanOrEqual(MEMBER_SEED_CONCURRENCY);
    expect(got).toHaveLength(0);
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
