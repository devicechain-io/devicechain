// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm channel's device-type enrichment, filter and counts (the alarm table's type tabs): the
// type of each row's originator is resolved with ONE batched read, remembered across polls; the type
// filter and the per-type counts are device-management's (the alarms query's deviceType facet and
// alarmCountsByDeviceType), so neither is capped by what the hub happens to read.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type AlarmSnapshot, type DeviceResolver } from './hub';
import type { AlarmRow, DatasourceSelector } from './types';

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

const countReads = () => h.gql.mock.calls.filter((c) => String(c[1]).includes('alarmCountsByDeviceType'));
const scopedHub = () =>
  new DashboardHub({ resolver: { devicesForAnchor: async () => ['therm-1', 'sens-1'], deviceExists: async () => true } });
const ANCHOR: DatasourceSelector = { kind: 'anchor', anchor: { targetType: 'area', targetToken: 'a', relationship: 'in' }, measurements: [] };

describe('the device-type filter', () => {
  it('is sent to device-management, and its page and total are the server’s', async () => {
    serve([row('a', 'therm-1'), row('c', 'therm-2')], 4321);
    const { snaps, dispose } = read({ pageSize: 10, deviceType: 'thermostat' });
    await settle();
    expect(alarmReads()).toHaveLength(1);
    expect(alarmReads()[0][2]).toMatchObject({ criteria: { pageNumber: 1, pageSize: 10, deviceType: 'thermostat' } });
    expect(snaps[0].alarms.map((a) => a.token)).toEqual(['a', 'c']);
    // No cap: the total is the server's match count, however large.
    expect(snaps[0].total).toBe(4321);
    dispose();
  });

  it('sends no deviceType facet when the subscription has none (negative control)', async () => {
    serve([row('a', 'therm-1')]);
    const { dispose } = read({ pageSize: 10 });
    await settle();
    expect(alarmReads()[0][2]).toMatchObject({ criteria: { deviceType: null } });
    dispose();
  });

  it('applies to each of a scoped widget’s originator reads', async () => {
    h.gql.mockImplementation(async () => page([]));
    const snaps: AlarmSnapshot[] = [];
    const dispose = scopedHub().subscribeAlarms({ datasource: ANCHOR, pageSize: 50, deviceType: 'sensor' }, { next: (s) => snaps.push(s) });
    await settle();
    expect(alarmReads().map((c) => (c[2] as { criteria: Record<string, unknown> }).criteria)).toEqual([
      expect.objectContaining({ originatorType: 'device', originator: 'therm-1', deviceType: 'sensor' }),
      expect.objectContaining({ originatorType: 'device', originator: 'sens-1', deviceType: 'sensor' }),
    ]);
    expect(snaps).toHaveLength(1);
    dispose();
  });
});

describe('the per-type counts', () => {
  const T = { token: 'thermostat', name: 'Thermostat' };
  const S = { token: 'sensor', name: 'Sensor' };

  it('are ONE grouped read tenant-wide, reading no alarm rows', async () => {
    h.gql.mockImplementation(async (_a: string, doc: string) => {
      if (String(doc).includes('alarmCountsByDeviceType')) {
        return { alarmCountsByDeviceType: [{ deviceType: T, severity: 'MAJOR', count: 3000 }, { deviceType: S, severity: 'MINOR', count: 2 }] };
      }
      throw new Error(`unexpected read ${String(doc)}`);
    });
    const { snaps, dispose } = read({ pageSize: 1, state: 'ACTIVE', countByDeviceType: true });
    await settle();
    expect(countReads()).toHaveLength(1);
    expect(countReads()[0][2]).toEqual({
      criteria: { state: 'ACTIVE', severity: null, acknowledged: null, originatorType: null, originator: null },
    });
    expect(alarmReads().filter((c) => !String(c[1]).includes('alarmCountsByDeviceType'))).toHaveLength(0);
    expect(snaps[0]).toEqual({
      alarms: [],
      total: 3002,
      typeCounts: [
        { deviceType: T, severity: 'MAJOR', count: 3000 },
        { deviceType: S, severity: 'MINOR', count: 2 },
      ],
    });
    dispose();
  });

  it('sum a scoped widget’s per-originator counts by type and severity', async () => {
    h.gql.mockImplementation(async (_a: string, _doc: string, vars: Record<string, unknown>) => {
      const o = (vars.criteria as { originator: string }).originator;
      return {
        alarmCountsByDeviceType:
          o === 'therm-1'
            ? [{ deviceType: T, severity: 'MAJOR', count: 2 }]
            : [{ deviceType: T, severity: 'MAJOR', count: 1 }, { deviceType: S, severity: 'CRITICAL', count: 4 }],
      };
    });
    const snaps: AlarmSnapshot[] = [];
    const dispose = scopedHub().subscribeAlarms({ datasource: ANCHOR, pageSize: 1, countByDeviceType: true }, { next: (s) => snaps.push(s) });
    await settle();
    expect(countReads()).toHaveLength(2);
    expect(snaps[0].typeCounts).toEqual([
      { deviceType: T, severity: 'MAJOR', count: 3 },
      { deviceType: S, severity: 'CRITICAL', count: 4 },
    ]);
    expect(snaps[0].total).toBe(7);
    dispose();
  });
});
