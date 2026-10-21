// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The event window of the alarm and command channels (a chart's event markers): the hub sends the
// range to device-management and command-delivery, which apply it before paging, so a snapshot's
// total is the count in range. The in-memory sources apply the same range the same way.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { alarmOverlaps, commandInRange, eventWindowBounds } from './event-window';
import { DashboardHub, type DeviceResolver } from './hub';
import { SyntheticDataSource } from './synthetic';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', () => ({
  subscribe: () => () => {},
  gql: (...args: unknown[]) => h.gql(...args),
}));

const NOW = Date.parse('2026-10-20T12:00:00Z');
const HOUR = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();
const resolver: DeviceResolver = { devicesForAnchor: async () => [], deviceExists: async () => true };
const device = { kind: 'device' as const, deviceToken: 'd-1', measurements: [] };
const criteria = (call = 0) => (h.gql.mock.calls[call][2] as { criteria: Record<string, unknown> }).criteria;
const settle = () => vi.advanceTimersByTimeAsync(5);

beforeEach(() => {
  h.gql.mockReset();
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});
afterEach(() => vi.useRealTimers());

describe('the hub sends the range', () => {
  it('a live alarm window is open at the top and slides with each read', async () => {
    h.gql.mockResolvedValue({ alarms: { results: [], pagination: { totalRecords: 7 } } });
    const snaps: Array<{ total: number }> = [];
    const dispose = new DashboardHub({ resolver }).subscribeAlarms(
      { datasource: device, pageSize: 30, window: { spanSeconds: 3600 } },
      { next: (s) => snaps.push(s) },
    );
    await settle();
    expect(criteria()).toMatchObject({ originator: 'd-1', pageSize: 30, startTime: iso(NOW - HOUR), endTime: null });
    expect(snaps[0].total).toBe(7);
    dispose();
  });

  it('a paused alarm window is closed at the pause', async () => {
    h.gql.mockResolvedValue({ alarms: { results: [], pagination: { totalRecords: 0 } } });
    const end = NOW - 30 * 24 * HOUR;
    const dispose = new DashboardHub({ resolver }).subscribeAlarms(
      { datasource: device, pageSize: 30, window: { spanSeconds: 86_400, endAt: end } },
      { next: () => {} },
    );
    await settle();
    expect(criteria()).toMatchObject({ startTime: iso(end - 24 * HOUR), endTime: iso(end) });
    dispose();
  });

  it('no window sends no range (the alarm table)', async () => {
    h.gql.mockResolvedValue({ alarms: { results: [], pagination: { totalRecords: 0 } } });
    const dispose = new DashboardHub({ resolver }).subscribeAlarms({ datasource: device, pageSize: 30 }, { next: () => {} });
    await settle();
    expect(criteria()).toMatchObject({ startTime: null, endTime: null });
    dispose();
  });

  it('the command window goes to command-delivery, with the total it counts', async () => {
    h.gql.mockResolvedValue({ commands: { results: [], pagination: { totalRecords: 12 } } });
    const snaps: Array<{ total: number }> = [];
    const dispose = new DashboardHub({ resolver }).subscribeCommands(
      { datasource: device, pageSize: 30, window: { spanSeconds: 7200, endAt: NOW - HOUR } },
      { next: (s) => snaps.push(s) },
    );
    await settle();
    expect(criteria()).toMatchObject({ deviceToken: 'd-1', pageSize: 30, startTime: iso(NOW - 3 * HOUR), endTime: iso(NOW - HOUR) });
    expect(snaps[0].total).toBe(12);
    dispose();
  });
});

describe('the range, applied in memory as the platform applies it', () => {
  const w = { spanSeconds: 2 * 3600, endAt: NOW }; // NOW-2h .. NOW
  it('bounds', () => {
    expect(eventWindowBounds(w, 0)).toEqual({ from: NOW - 2 * HOUR, to: NOW });
    expect(eventWindowBounds({ spanSeconds: 60 }, NOW)).toEqual({ from: NOW - 60_000, to: undefined });
  });

  it('an alarm overlaps: raised before and still active, or cleared inside, counts', () => {
    const a = (raised: number, cleared: number | null) => ({ raisedTime: iso(raised), clearedTime: cleared === null ? null : iso(cleared) });
    expect(alarmOverlaps(a(NOW - 5 * HOUR, null), w, NOW)).toBe(true);
    expect(alarmOverlaps(a(NOW - 5 * HOUR, NOW - HOUR), w, NOW)).toBe(true);
    expect(alarmOverlaps(a(NOW - 5 * HOUR, NOW - 3 * HOUR), w, NOW)).toBe(false);
    expect(alarmOverlaps(a(NOW + HOUR, null), w, NOW)).toBe(false);
    expect(alarmOverlaps({ raisedTime: null, clearedTime: null }, w, NOW)).toBe(false);
  });

  it('a command is placed at its send, or its queue time when never sent', () => {
    expect(commandInRange({ sentTime: iso(NOW - HOUR), queuedTime: iso(NOW - 5 * HOUR) }, w, NOW)).toBe(true);
    expect(commandInRange({ sentTime: iso(NOW + HOUR), queuedTime: iso(NOW - HOUR) }, w, NOW)).toBe(false);
    expect(commandInRange({ sentTime: null, queuedTime: iso(NOW - HOUR) }, w, NOW)).toBe(true);
    expect(commandInRange({ sentTime: null, queuedTime: iso(NOW - 3 * HOUR) }, w, NOW)).toBe(false);
  });

  it('the synthetic preview counts only its rows in range', () => {
    const source = new SyntheticDataSource();
    let all = 0;
    let none = -1;
    source.subscribeCommands({ pageSize: 100 }, { next: (s) => (all = s.total) })();
    source.subscribeCommands({ pageSize: 100, window: { spanSeconds: 60, endAt: NOW - 24 * HOUR } }, { next: (s) => (none = s.total) })();
    expect(all).toBeGreaterThan(0);
    expect(none).toBe(0);
  });
});
