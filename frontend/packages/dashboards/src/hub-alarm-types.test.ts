// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm channel's device-type enrichment and filter (the alarm table's type tabs): the type of
// each row's originator is resolved with ONE batched read, remembered across polls, and a type
// filter narrows the rows inside the hub, because the alarms query has no device facet.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type AlarmSnapshot, type DeviceResolver } from './hub';
import type { AlarmRow } from './types';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', () => ({
  subscribe: () => () => {},
  gql: (...args: unknown[]) => h.gql(...args),
}));

const row = (token: string, device: string, over: Partial<AlarmRow> = {}): AlarmRow => ({
  token,
  originatorType: 'device',
  originatorToken: device,
  alarmKey: 'k',
  metricKey: 'm',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'MAJOR',
  raisedTime: '2026-07-06T00:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 1,
  ...over,
});

const page = (rows: AlarmRow[], total = rows.length) => ({ alarms: { results: rows, pagination: { totalRecords: total } } });

const TYPES: Record<string, { token: string; name: string | null }> = {
  'therm-1': { token: 'thermostat', name: 'Thermostat' },
  'therm-2': { token: 'thermostat', name: 'Thermostat' },
  'sens-1': { token: 'sensor', name: 'Sensor' },
};

// Answers the alarms query with `alarms` (paged by criteria.pageNumber/pageSize) and the device-type
// read from TYPES.
function serve(alarms: AlarmRow[], total = alarms.length) {
  h.gql.mockImplementation(async (_area: string, doc: string, vars: Record<string, unknown>) => {
    if (String(doc).includes('devicesByToken')) {
      const tokens = vars.tokens as string[];
      return { devicesByToken: tokens.filter((t) => TYPES[t]).map((t) => ({ token: t, deviceType: TYPES[t] })) };
    }
    const c = vars.criteria as { pageNumber: number; pageSize: number };
    const start = (c.pageNumber - 1) * c.pageSize;
    return page(alarms.slice(start, start + c.pageSize), total);
  });
}

const typeReads = () => h.gql.mock.calls.filter((c) => String(c[1]).includes('devicesByToken'));
const alarmReads = () => h.gql.mock.calls.filter((c) => !String(c[1]).includes('devicesByToken'));
const resolver: DeviceResolver = { devicesForAnchor: async () => [], deviceExists: async () => true };
const settle = () => vi.advanceTimersByTimeAsync(5);

beforeEach(() => {
  h.gql.mockReset();
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

function read(sub: Parameters<DashboardHub['subscribeAlarms']>[0]) {
  const hub = new DashboardHub({ resolver });
  const snaps: AlarmSnapshot[] = [];
  const dispose = hub.subscribeAlarms(sub, { next: (s) => snaps.push(s) });
  return { hub, snaps, dispose };
}

describe('alarm rows with their device type', () => {
  it('resolves the types of all originators with ONE batched read', async () => {
    serve([row('a', 'therm-1'), row('b', 'therm-2'), row('c', 'sens-1'), row('d', 'therm-1')]);
    const { snaps, dispose } = read({ pageSize: 50, enrichDeviceType: true });
    await settle();

    expect(typeReads()).toHaveLength(1);
    expect(typeReads()[0][2]).toEqual({ tokens: ['therm-1', 'therm-2', 'sens-1'] });
    expect(snaps[0].alarms.map((a) => a.deviceType?.token)).toEqual(['thermostat', 'thermostat', 'sensor', 'thermostat']);
    dispose();
  });

  it('reads nothing extra when the subscription does not ask', async () => {
    serve([row('a', 'therm-1')]);
    const { snaps, dispose } = read({ pageSize: 50 });
    await settle();
    expect(typeReads()).toHaveLength(0);
    expect(snaps[0].alarms[0]).not.toHaveProperty('deviceType');
    dispose();
  });

  it('remembers a device’s type, so a later poll reads only the devices it has not seen', async () => {
    serve([row('a', 'therm-1')]);
    const { dispose } = read({ pageSize: 50, enrichDeviceType: true });
    await settle();
    expect(typeReads()).toHaveLength(1);

    serve([row('a', 'therm-1'), row('c', 'sens-1')]);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(typeReads()).toHaveLength(2);
    expect(typeReads()[1][2]).toEqual({ tokens: ['sens-1'] });
    dispose();
  });

  it('leaves a row unresolved, not the whole table failed, when the type read fails', async () => {
    h.gql.mockImplementation(async (_a: string, doc: string) => {
      if (String(doc).includes('devicesByToken')) throw new Error('boom');
      return page([row('a', 'therm-1')]);
    });
    const { snaps, dispose } = read({ pageSize: 50, enrichDeviceType: true });
    await settle();
    expect(snaps).toHaveLength(1);
    expect(snaps[0].alarms[0].deviceType).toBeUndefined();
    dispose();
  });

  it('marks a non-device originator as having no device type (null), not unresolved', async () => {
    serve([row('a', 'x', { originatorType: 'asset', originatorToken: null })]);
    const { snaps, dispose } = read({ pageSize: 50, enrichDeviceType: true });
    await settle();
    expect(snaps[0].alarms[0].deviceType).toBeNull();
    expect(typeReads()).toHaveLength(0);
    dispose();
  });
});

describe('the device-type filter', () => {
  it('keeps only that type’s alarms, and reports the matches as the total', async () => {
    serve([row('a', 'therm-1'), row('b', 'sens-1'), row('c', 'therm-2')]);
    const { snaps, dispose } = read({ pageSize: 50, deviceType: 'thermostat' });
    await settle();
    expect(snaps[0].alarms.map((a) => a.token)).toEqual(['a', 'c']);
    expect(snaps[0].total).toBe(2);
    dispose();
  });

  it('pages through the matching alarms to find the type, up to its scan cap', async () => {
    const all = Array.from({ length: 1100 }, (_, i) => row(`r${i}`, i % 2 ? 'therm-1' : 'sens-1'));
    serve(all);
    const { snaps, dispose } = read({ pageSize: 10, deviceType: 'thermostat' });
    await settle();
    // 500-row pages: three reads cover 1100 alarms, none beyond the total.
    expect(alarmReads()).toHaveLength(3);
    expect(snaps[0].total).toBe(550);
    expect(snaps[0].alarms).toHaveLength(10);
    dispose();
  });

  it('stops at the scan cap rather than paging an unbounded alarm history', async () => {
    const all = Array.from({ length: 5000 }, (_, i) => row(`r${i}`, 'therm-1'));
    serve(all);
    const { snaps, dispose } = read({ pageSize: 10, deviceType: 'thermostat' });
    await settle();
    expect(alarmReads()).toHaveLength(4);
    expect(snaps[0].total).toBe(2000);
    dispose();
  });

  it('applies to a scoped widget’s merged rows too', async () => {
    const scoped = new DashboardHub({
      resolver: { devicesForAnchor: async () => ['therm-1', 'sens-1'], deviceExists: async () => true },
    });
    h.gql.mockImplementation(async (_a: string, doc: string, vars: Record<string, unknown>) => {
      if (String(doc).includes('devicesByToken')) {
        return { devicesByToken: (vars.tokens as string[]).map((t) => ({ token: t, deviceType: TYPES[t] })) };
      }
      const device = (vars.criteria as { originator: string }).originator;
      return page([row(`alarm-${device}`, device)]);
    });
    const snaps: AlarmSnapshot[] = [];
    const dispose = scoped.subscribeAlarms(
      {
        datasource: { kind: 'anchor', anchor: { targetType: 'area', targetToken: 'a', relationship: 'in' }, measurements: [] },
        pageSize: 50,
        deviceType: 'sensor',
      },
      { next: (s) => snaps.push(s) },
    );
    await settle();
    expect(snaps[0].alarms.map((a) => a.token)).toEqual(['alarm-sens-1']);
    dispose();
  });
});
