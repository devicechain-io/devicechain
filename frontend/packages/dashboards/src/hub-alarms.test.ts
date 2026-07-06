// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type AlarmSnapshot, type DeviceResolver } from './hub';
import type { AlarmRow } from './types';

// Controllable fakes of the SDK: subscribe() records each opened trigger stream so a
// test can push events and assert teardown; gql() is a vi.fn a test drives to return
// alarm pages (or reject). vi.hoisted shares the registry with the hoisted vi.mock.
const h = vi.hoisted(() => ({
  subs: [] as Array<{ sink: { next: (d: unknown) => void; connected?: (retry: boolean) => void }; closed: boolean }>,
  gql: vi.fn(),
}));

vi.mock('@devicechain/client', () => ({
  subscribe: (
    _area: string,
    _doc: unknown,
    _vars: unknown,
    sink: { next: (d: unknown) => void; connected?: (retry: boolean) => void },
  ) => {
    const entry = { sink, closed: false };
    h.subs.push(entry);
    return () => {
      entry.closed = true;
    };
  },
  gql: (...args: unknown[]) => h.gql(...args),
}));

function alarmRow(over: Partial<AlarmRow> = {}): AlarmRow {
  return {
    token: 'alarm-1',
    originatorType: 'device',
    originatorToken: 'therm-1',
    alarmKey: 'over-temp',
    metricKey: 'temperature',
    state: 'ACTIVE',
    acknowledged: false,
    severity: 'CRITICAL',
    raisedTime: '2026-07-06T00:00:00Z',
    clearedTime: null,
    acknowledgedTime: null,
    acknowledgedBy: null,
    lastValue: 90,
    message: null,
    ...over,
  };
}

// The alarms-query result shape gql resolves to.
function page(rows: AlarmRow[], total = rows.length) {
  return { alarms: { results: rows, pagination: { totalRecords: total } } };
}

function newResolver(devices: string[] = []): DeviceResolver {
  return { devicesForAnchor: vi.fn(async () => devices) };
}

// The criteria of the Nth gql() call (call args are [area, doc, { criteria }]).
function criteriaOf(call: number): Record<string, unknown> {
  return (h.gql.mock.calls[call][2] as { criteria: Record<string, unknown> }).criteria;
}

// Advance a tick so the async scope-resolution + query microtask chain settles (no
// timer fires under 800ms, the debounce floor).
const settle = () => vi.advanceTimersByTimeAsync(5);

beforeEach(() => {
  h.subs.length = 0;
  h.gql.mockReset();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('DashboardHub alarm channel', () => {
  it('tenant-wide reads all alarms unfiltered and reports the server total', async () => {
    h.gql.mockResolvedValue(page([alarmRow({ token: 'a' })], 7));
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: AlarmSnapshot[] = [];

    hub.subscribeAlarms({ pageSize: 50 }, { next: (s) => snaps.push(s) });
    await settle();

    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(criteriaOf(0).originator).toBeNull();
    expect(criteriaOf(0).originatorType).toBeNull();
    expect(snaps[snaps.length - 1]).toEqual({ alarms: [expect.objectContaining({ token: 'a' })], total: 7 });
    // A tenant-wide subscription opens one trigger stream.
    expect(h.subs).toHaveLength(1);
  });

  it('device scope filters the query by originator', async () => {
    h.gql.mockResolvedValue(page([alarmRow()]));
    const hub = new DashboardHub({ resolver: newResolver() });

    hub.subscribeAlarms(
      { datasource: { kind: 'device', deviceToken: 'therm-1', measurements: [] }, pageSize: 10 },
      { next: () => {} },
    );
    await settle();

    expect(criteriaOf(0).originatorType).toBe('device');
    expect(criteriaOf(0).originator).toBe('therm-1');
  });

  it('passes state/severity filters through to the query', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });

    hub.subscribeAlarms({ pageSize: 10, state: 'ACTIVE', severity: 'CRITICAL' }, { next: () => {} });
    await settle();

    expect(criteriaOf(0).state).toBe('ACTIVE');
    expect(criteriaOf(0).severity).toBe('CRITICAL');
  });

  it('a scoped widget that resolves to no device shows empty — NOT tenant-wide', async () => {
    // A slot with no binding resolves to zero devices. It must render empty, never fall
    // through to a tenant-wide query (which would leak every alarm into an unbound tile).
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: AlarmSnapshot[] = [];

    hub.subscribeAlarms(
      { datasource: { kind: 'slot', slot: 'unbound', measurements: [] }, pageSize: 10 },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    expect(h.gql).not.toHaveBeenCalled();
    expect(h.subs).toHaveLength(0);
    expect(snaps).toEqual([{ alarms: [], total: 0 }]);
  });

  it('a live event triggers a debounced re-query', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeAlarms({ pageSize: 10 }, { next: () => {} });
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1); // initial load

    h.subs[0].sink.next({ alarmStream: { alarmToken: 'a', eventType: 'RAISED' } });
    // Not yet — still inside the debounce window.
    await vi.advanceTimersByTimeAsync(500);
    expect(h.gql).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(400);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('coalesces a burst of events into one re-query', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeAlarms({ pageSize: 10 }, { next: () => {} });
    await settle();

    const evt = { alarmStream: { alarmToken: 'a', eventType: 'RAISED' } };
    h.subs[0].sink.next(evt);
    h.subs[0].sink.next(evt);
    h.subs[0].sink.next(evt);
    await vi.advanceTimersByTimeAsync(900);

    expect(h.gql).toHaveBeenCalledTimes(2); // initial + one coalesced reconcile
  });

  it('polls as a correctness backstop', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeAlarms({ pageSize: 10 }, { next: () => {} });
    await settle();

    await vi.advanceTimersByTimeAsync(30_000);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('re-queries on reconnect (a retried socket)', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeAlarms({ pageSize: 10 }, { next: () => {} });
    await settle();

    h.subs[0].sink.connected?.(true); // reconnected after a drop
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(2);

    h.subs[0].sink.connected?.(false); // first connect — not a retry, no re-query
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('anchor scope merges per-device queries, dedups by token, sums totals', async () => {
    // Two member devices both report the same alarm token (deduped) plus counts.
    h.gql.mockResolvedValue(page([alarmRow({ token: 'shared' })], 2));
    const hub = new DashboardHub({ resolver: newResolver(['d1', 'd2']) });
    const snaps: AlarmSnapshot[] = [];

    hub.subscribeAlarms(
      {
        datasource: {
          kind: 'anchor',
          anchor: { relationship: 'monitors', targetType: 'area', targetToken: 'a1' },
          measurements: [],
        },
        pageSize: 10,
      },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    expect(h.gql).toHaveBeenCalledTimes(2); // one query per member device
    expect(snaps[snaps.length - 1].alarms).toHaveLength(1); // deduped by token
    expect(snaps[snaps.length - 1].total).toBe(4); // 2 + 2
  });

  it('dispose stops polling and closes the trigger stream', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const dispose = hub.subscribeAlarms({ pageSize: 10 }, { next: () => {} });
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1);

    dispose();
    expect(h.subs[0].closed).toBe(true);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.gql).toHaveBeenCalledTimes(1); // no further polls
  });

  it('surfaces a query error to the sink', async () => {
    h.gql.mockRejectedValue(new Error('boom'));
    const hub = new DashboardHub({ resolver: newResolver() });
    const errors: unknown[] = [];

    hub.subscribeAlarms({ pageSize: 10 }, { next: () => {}, error: (e) => errors.push(e) });
    await settle();

    expect(errors).toHaveLength(1);
    expect((errors[0] as Error).message).toBe('boom');
  });
});
