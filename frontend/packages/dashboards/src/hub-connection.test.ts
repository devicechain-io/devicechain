// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The hub folds its sockets' connected/closed events into one state a header can show.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { OFFLINE_AFTER_MS, STABLE_AFTER_MS } from './connection';
import { DashboardHub, type DeviceResolver } from './hub';

interface FakeSink {
  connected?: (wasRetry: boolean) => void;
  closed?: () => void;
  error?: (e: unknown) => void;
}

const h = vi.hoisted(() => ({ sinks: [] as FakeSink[] }));

vi.mock('@devicechain/client', () => ({
  subscribe: (_area: string, _doc: unknown, _vars: unknown, sink: FakeSink) => {
    h.sinks.push(sink);
    return () => {};
  },
  gql: vi.fn(async () => ({ alarms: { results: [], pagination: { totalRecords: 0 } } })),
  isForbiddenError: () => false,
}));

const resolver: DeviceResolver = { devicesForAnchor: vi.fn(async () => []), deviceExists: vi.fn(async () => true) };

beforeEach(() => {
  h.sinks.length = 0;
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

async function openStream(hub: DashboardHub): Promise<FakeSink> {
  hub.subscribeWidget({ kind: 'device', deviceToken: 'd1', measurements: ['t'] }, { next: vi.fn() });
  await vi.advanceTimersByTimeAsync(0);
  expect(h.sinks).toHaveLength(1);
  return h.sinks[0];
}

describe('DashboardHub connection state', () => {
  it('is live with nothing subscribed', () => {
    expect(new DashboardHub({ resolver }).connectionState).toBe('live');
  });

  it('follows a measurement stream socket: closed -> reconnecting -> offline, connected -> live', async () => {
    const hub = new DashboardHub({ resolver });
    const listener = vi.fn();
    hub.onConnectionState(listener);
    const sink = await openStream(hub);

    sink.closed?.();
    expect(hub.connectionState).toBe('reconnecting');
    vi.advanceTimersByTime(OFFLINE_AFTER_MS);
    expect(hub.connectionState).toBe('offline');
    sink.connected?.(true);
    expect(hub.connectionState).toBe('live');
    expect(listener).toHaveBeenCalledTimes(3);
  });

  it('stops counting a stream that is torn down while down', async () => {
    const hub = new DashboardHub({ resolver });
    const sink = await openStream(hub);
    sink.closed?.();
    expect(hub.connectionState).toBe('reconnecting');
    hub.disposeAll();
    expect(hub.connectionState).toBe('live');
  });

  it('follows the alarm stream socket too, and releases it when the widget goes', async () => {
    const hub = new DashboardHub({ resolver });
    const dispose = hub.subscribeAlarms({ pageSize: 10 }, { next: vi.fn(), error: vi.fn() });
    await vi.advanceTimersByTimeAsync(0);
    expect(h.sinks).toHaveLength(1);
    const sink = h.sinks[0];

    sink.closed?.();
    expect(hub.connectionState).toBe('reconnecting');
    vi.advanceTimersByTime(OFFLINE_AFTER_MS);
    expect(hub.connectionState).toBe('offline');
    sink.connected?.(true);
    expect(hub.connectionState).toBe('live');
    vi.advanceTimersByTime(STABLE_AFTER_MS);

    sink.closed?.();
    expect(hub.connectionState).toBe('reconnecting');
    dispose();
    expect(hub.connectionState).toBe('live');
  });

  it('keeps a stream ended by a fatal close counted as down until its widget goes', async () => {
    const hub = new DashboardHub({ resolver });
    const widgetSink = { next: vi.fn(), error: vi.fn() };
    const detach = hub.subscribeWidget({ kind: 'device', deviceToken: 'd1', measurements: ['t'] }, widgetSink);
    await vi.advanceTimersByTimeAsync(0);
    const sink = h.sinks[0];

    sink.closed?.();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS);
    // A fatal close errors the operation: the board must not now read live.
    sink.error?.(new Error('socket closed'));
    expect(widgetSink.error).toHaveBeenCalled();
    expect(hub.connectionState).toBe('offline');
    detach();
    expect(hub.connectionState).toBe('live');
  });

  it('a dead stream is released by disposeAll as well', async () => {
    const hub = new DashboardHub({ resolver });
    const sink = await openStream(hub);
    sink.closed?.();
    sink.error?.(new Error('socket closed'));
    expect(hub.connectionState).toBe('reconnecting');
    hub.disposeAll();
    expect(hub.connectionState).toBe('live');
  });

  it('a socket that drops again before it is stable resumes the offline clock', async () => {
    const hub = new DashboardHub({ resolver });
    const sink = await openStream(hub);

    sink.closed?.();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS - 5_000);
    sink.connected?.(true);
    expect(hub.connectionState).toBe('live');
    vi.advanceTimersByTime(2_000);
    sink.closed?.();
    expect(hub.connectionState).toBe('reconnecting');
    vi.advanceTimersByTime(5_000);
    expect(hub.connectionState).toBe('offline');

    // Up long enough to be stable: the next drop starts a fresh clock.
    sink.connected?.(true);
    vi.advanceTimersByTime(STABLE_AFTER_MS);
    sink.closed?.();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS - 1);
    expect(hub.connectionState).toBe('reconnecting');
    vi.advanceTimersByTime(1);
    expect(hub.connectionState).toBe('offline');
  });
});
