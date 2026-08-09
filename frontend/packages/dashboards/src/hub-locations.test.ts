// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver, type LocationSnapshot } from './hub';
import type { DatasourceSelector, LocationSample } from './types';

// Controllable fakes of the SDK's WIRE only: gql() is a vi.fn a test drives, and
// subscribe() is recorded so a test can assert the location channel opens NO socket
// (device-state has no location subscription — if one ever appeared here it would be a
// stream nothing reads). Everything else comes from the REAL module, which matters for
// one function in particular: isForbiddenError is the thing that decides whether a
// refusal becomes a state or an error, and a stubbed classifier would let the forbidden
// test below pass against a hub that classifies nothing.
const h = vi.hoisted(() => ({
  subs: [] as Array<{ closed: boolean }>,
  gql: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    subscribe: () => {
      const entry = { closed: false };
      h.subs.push(entry);
      return () => {
        entry.closed = true;
      };
    },
    gql: (...args: unknown[]) => h.gql(...args),
  };
});

// Imported AFTER the mock factory is declared (vi.mock is hoisted, so this still gets
// the real class — it is one of the exports the factory passes through).
import { GraphQLRequestError } from '@devicechain/client';

function sample(over: Partial<LocationSample> = {}): LocationSample {
  return {
    id: 'loc-1',
    deviceToken: 'dozer-1',
    latitude: 33.749,
    longitude: -84.388,
    elevation: 320.5,
    accuracy: 4.2,
    speed: 0,
    heading: 271.5,
    occurredTime: '2026-08-09T12:00:00Z',
    ...over,
  };
}

// The latestLocations-query result shape gql resolves to.
function page(rows: LocationSample[]) {
  return { latestLocations: rows };
}

function newResolver(devices: string[] = []): DeviceResolver {
  return { devicesForAnchor: vi.fn(async () => devices), deviceExists: vi.fn(async () => true) };
}

// The variables of the Nth gql() call (call args are [area, doc, variables]).
function varsOf(call: number): { deviceTokens: string[] } {
  return h.gql.mock.calls[call][2] as { deviceTokens: string[] };
}

function areaOf(call: number): string {
  return h.gql.mock.calls[call][0] as string;
}

// A device selector that DOES name the location series — what a bound map widget carries.
function locatedDevice(deviceToken: string): DatasourceSelector {
  return { kind: 'device', deviceToken, measurements: [], location: { series: 'latest' } };
}

// Advance a tick so the async scope-resolution + query microtask chain settles (no timer
// fires under 15s, the poll interval).
const settle = () => vi.advanceTimersByTimeAsync(5);

beforeEach(() => {
  h.subs.length = 0;
  h.gql.mockReset();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('DashboardHub location channel', () => {
  it('reads the bound device position from device-state and reports it', async () => {
    h.gql.mockResolvedValue(page([sample()]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: LocationSnapshot[] = [];

    hub.subscribeLocations({ datasource: locatedDevice('dozer-1') }, { next: (s) => snaps.push(s) });
    await settle();

    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(areaOf(0)).toBe('device-state');
    expect(varsOf(0).deviceTokens).toEqual(['dozer-1']);
    expect(snaps[snaps.length - 1]).toEqual({
      kind: 'positions',
      deviceTokens: ['dozer-1'],
      locations: [expect.objectContaining({ deviceToken: 'dozer-1', latitude: 33.749, longitude: -84.388 })],
    });
    // The location channel is poll-only: it must open no subscription at all.
    expect(h.subs).toHaveLength(0);
  });

  // 🔴 The load-bearing property of the separate location field. Without this, the field
  // is decorative: every map bound to any selector would read positions whether or not a
  // location series was authored, so nothing would ever hold the shape the ADR asked for.
  it('resolves NOTHING for a selector that names no location series', async () => {
    const hub = new DashboardHub({ resolver: newResolver(['d1', 'd2']) });
    const snaps: LocationSnapshot[] = [];

    hub.subscribeLocations(
      // A perfectly valid measurement binding on the same device — but it names no
      // location series, so it is a telemetry selector and the map has nothing to plot.
      { datasource: { kind: 'device', deviceToken: 'dozer-1', measurements: ['temperature'] } },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    expect(h.gql).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ kind: 'positions', deviceTokens: [], locations: [] }]);
  });

  it('an anchor naming the location series reads every member device in ONE batch query', async () => {
    h.gql.mockResolvedValue(page([sample({ deviceToken: 'd1' }), sample({ id: 'loc-2', deviceToken: 'd2' })]));
    const hub = new DashboardHub({ resolver: newResolver(['d1', 'd2']) });
    const snaps: LocationSnapshot[] = [];

    hub.subscribeLocations(
      {
        datasource: {
          kind: 'anchor',
          anchor: { relationship: 'monitors', targetType: 'area', targetToken: 'site-1' },
          measurements: [],
          location: { series: 'latest' },
        },
      },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    // ONE query for the whole fleet — the point of the batch form. (The alarm channel
    // runs one query per device; this must not.)
    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(varsOf(0).deviceTokens).toEqual(['d1', 'd2']);
    const snap = snaps[snaps.length - 1];
    expect(snap.kind).toBe('positions');
    expect(snap.kind === 'positions' && snap.locations.map((l) => l.deviceToken)).toEqual(['d1', 'd2']);
  });

  it('a slot selector naming the location series resolves through the binding manifest', async () => {
    h.gql.mockResolvedValue(page([sample({ deviceToken: 'bound-device' })]));
    const hub = new DashboardHub({
      resolver: newResolver(),
      bindings: { fleet: { kind: 'device', deviceToken: 'bound-device' } },
    });

    hub.subscribeLocations(
      { datasource: { kind: 'slot', slot: 'fleet', measurements: [], location: { series: 'latest' } } },
      { next: () => {} },
    );
    await settle();

    expect(varsOf(0).deviceTokens).toEqual(['bound-device']);
  });

  it('an unbound slot shows empty — never a refusal', async () => {
    // A template whose host bound nothing resolves to zero devices. It must NOT read as
    // "you are not permitted": there is no permission problem, there is no device.
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: LocationSnapshot[] = [];

    hub.subscribeLocations(
      { datasource: { kind: 'slot', slot: 'unbound', measurements: [], location: { series: 'latest' } } },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    expect(h.gql).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ kind: 'positions', deviceTokens: [], locations: [] }]);
  });

  // A never-located device is ABSENT from the query result. The snapshot carries the
  // resolved tokens alongside, so "bound to nothing" stays distinguishable from "bound to
  // devices that have never reported" — two different things to tell an operator.
  it('reports the bound devices even when none of them has ever been located', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver(['d1', 'd2']) });
    const snaps: LocationSnapshot[] = [];

    hub.subscribeLocations(
      {
        datasource: {
          kind: 'anchor',
          anchor: { relationship: 'monitors', targetType: 'area', targetToken: 'site-1' },
          measurements: [],
          location: { series: 'latest' },
        },
      },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    expect(snaps[snaps.length - 1]).toEqual({
      kind: 'positions',
      deviceTokens: ['d1', 'd2'],
      locations: [],
    });
  });

  it('polls the positions on its cadence', async () => {
    h.gql.mockResolvedValue(page([sample()]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeLocations({ datasource: locatedDevice('dozer-1') }, { next: () => {} });
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1); // initial load

    // Nothing before the interval elapses…
    await vi.advanceTimersByTimeAsync(14_000);
    expect(h.gql).toHaveBeenCalledTimes(1);
    // …and exactly one re-read after it.
    await vi.advanceTimersByTimeAsync(1_500);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('dispose stops polling', async () => {
    h.gql.mockResolvedValue(page([sample()]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const dispose = hub.subscribeLocations({ datasource: locatedDevice('dozer-1') }, { next: () => {} });
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1);

    dispose();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(h.gql).toHaveBeenCalledTimes(1); // no further polls
  });

  it('a disposed subscription delivers no further snapshots', async () => {
    // Teardown mid-flight: the query is already in the air when the widget unmounts.
    let resolveQuery: ((v: unknown) => void) | undefined;
    h.gql.mockReturnValue(new Promise((r) => { resolveQuery = r; }));
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: LocationSnapshot[] = [];
    const dispose = hub.subscribeLocations(
      { datasource: locatedDevice('dozer-1') },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    dispose();
    resolveQuery?.(page([sample()]));
    await settle();

    expect(snaps).toEqual([]);
  });

  it('disposeAll tears down location subscriptions', async () => {
    h.gql.mockResolvedValue(page([sample()]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeLocations({ datasource: locatedDevice('dozer-1') }, { next: () => {} });
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1);

    hub.disposeAll();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(h.gql).toHaveBeenCalledTimes(1); // poll stopped
  });

  // 🔴 The refusal is the reason this channel has a discriminated snapshot at all.
  // location:read is deliberately outside the read-only viewer baseline, so an ordinary
  // member IS refused — routinely, by design. It must arrive as a STATE.
  it('turns a location:read refusal into a snapshot state, not an error', async () => {
    h.gql.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'forbidden: missing required authority' }]),
    );
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: LocationSnapshot[] = [];
    const errors: unknown[] = [];

    hub.subscribeLocations(
      { datasource: locatedDevice('dozer-1') },
      { next: (s) => snaps.push(s), error: (e) => errors.push(e) },
    );
    await settle();

    expect(snaps).toEqual([{ kind: 'forbidden' }]);
    expect(errors).toEqual([]);
  });

  // The counterweight: classifying a refusal as a state is only safe while a real
  // failure still fails. Otherwise every outage would render as a permission boundary
  // and nobody would ever be paged.
  it('surfaces a NON-refusal query failure to the error sink', async () => {
    h.gql.mockRejectedValue(new GraphQLRequestError('connection reset', 0));
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: LocationSnapshot[] = [];
    const errors: unknown[] = [];

    hub.subscribeLocations(
      { datasource: locatedDevice('dozer-1') },
      { next: (s) => snaps.push(s), error: (e) => errors.push(e) },
    );
    await settle();

    expect(snaps).toEqual([]);
    expect(errors).toHaveLength(1);
    expect((errors[0] as Error).message).toBe('connection reset');
  });

  it('surfaces a scope-resolution failure to the error sink', async () => {
    const resolver: DeviceResolver = {
      devicesForAnchor: vi.fn(async () => {
        throw new Error('anchor lookup failed');
      }),
      deviceExists: vi.fn(async () => true),
    };
    const hub = new DashboardHub({ resolver });
    const errors: unknown[] = [];

    hub.subscribeLocations(
      {
        datasource: {
          kind: 'anchor',
          anchor: { relationship: 'monitors', targetType: 'area', targetToken: 'site-1' },
          measurements: [],
          location: { series: 'latest' },
        },
      },
      { next: () => {}, error: (e) => errors.push(e) },
    );
    await settle();

    expect(errors).toHaveLength(1);
    expect((errors[0] as Error).message).toBe('anchor lookup failed');
  });
});
