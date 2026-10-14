// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The hub folds its sockets' connected/closed events into one state a header can show.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { OFFLINE_AFTER_MS } from './connection';
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
  gql: vi.fn(),
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
});
