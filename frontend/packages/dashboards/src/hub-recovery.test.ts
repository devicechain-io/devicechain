// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A live board survives a long outage. These drive the hub through the REAL client and the
// real graphql-ws over a fake WebSocket that behaves as a browser's does: a handshake that
// cannot complete fires `error` and then `close` (1006). The hub-connection tests script the
// client instead, which is how the defect these pin went unseen: graphql-ws gave up on its own
// after a few attempts (at once, in a browser, on the first `error`), errored every operation,
// and nothing reopened them, so a wall display read "Data unavailable" until it was reloaded.

import { disposeSubscriptions } from '@devicechain/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver } from './hub';

interface Frame {
  id?: string;
  type: string;
  payload?: unknown;
}

// The server side of every connection. `down` refuses the handshake the way a browser
// reports an unreachable server; `refuseInit` closes connection_init with 4401.
class FakeSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static all: FakeSocket[] = [];
  static down = false;
  static refuseInit = false;
  // The most sockets ever connecting or open at one time.
  static peak = 0;

  readyState = FakeSocket.CONNECTING;
  at = Date.now();
  subscriptions: Frame[] = [];
  onopen: (() => void) | null = null;
  onclose: ((e: { code: number; reason: string; wasClean: boolean }) => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;

  constructor(public url: string) {
    FakeSocket.all.push(this);
    FakeSocket.peak = Math.max(FakeSocket.peak, FakeSocket.all.filter((s) => s.readyState !== FakeSocket.CLOSED).length);
    setTimeout(() => {
      if (FakeSocket.down) {
        this.readyState = FakeSocket.CLOSED;
        this.onerror?.(new Event('error'));
        this.onclose?.({ code: 1006, reason: '', wasClean: false });
        return;
      }
      this.readyState = FakeSocket.OPEN;
      this.onopen?.();
    }, 0);
  }

  send(data: string): void {
    const frame = JSON.parse(data) as Frame;
    if (frame.type === 'connection_init') {
      setTimeout(() => {
        if (FakeSocket.refuseInit) this.drop(4401);
        else this.deliver({ type: 'connection_ack' });
      }, 0);
    } else if (frame.type === 'subscribe') {
      this.subscriptions.push(frame);
    }
  }

  deliver(frame: Frame): void {
    if (this.readyState === FakeSocket.OPEN) this.onmessage?.({ data: JSON.stringify(frame) });
  }

  // The server pushes one reading on every subscription this socket carries.
  publish(value: number): void {
    for (const sub of this.subscriptions) {
      const deviceToken = (sub.payload as { variables: { deviceToken: string } }).variables.deviceToken;
      this.deliver({
        id: sub.id,
        type: 'next',
        payload: {
          data: {
            measurementStream: { id: `${deviceToken}-${value}`, deviceToken, eventType: 0, occurredTime: '2026-10-15T00:00:00Z', name: 't', value, classifier: null },
          },
        },
      });
    }
  }

  // The connection ends, as the server or the network ends it.
  drop(code: number): void {
    this.readyState = FakeSocket.CLOSED;
    this.onclose?.({ code, reason: '', wasClean: code !== 1006 });
  }

  close(): void {
    this.readyState = FakeSocket.CLOSED;
  }
}

const live = (): FakeSocket => {
  const open = FakeSocket.all.filter((s) => s.readyState === FakeSocket.OPEN);
  expect(open).toHaveLength(1);
  return open[0];
};

const resolver: DeviceResolver = { devicesForAnchor: vi.fn(async () => []), deviceExists: vi.fn(async () => true) };

interface Widget {
  values: number[];
  errors: unknown[];
}

function openWidgets(hub: DashboardHub, count: number): Widget[] {
  return Array.from({ length: count }, (_, i) => {
    const w: Widget = { values: [], errors: [] };
    hub.subscribeWidget(
      { kind: 'device', deviceToken: `d${i}`, measurements: ['t'] },
      { next: (s) => w.values.push(s.value as number), error: (e) => w.errors.push(e) },
    );
    return w;
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeSocket.all = [];
  FakeSocket.down = false;
  FakeSocket.refuseInit = false;
  FakeSocket.peak = 0;
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.stubGlobal('window', { location: { protocol: 'http:', host: 'localhost' } });
});

afterEach(() => {
  disposeSubscriptions();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('a live board after a long disconnection', () => {
  it('gets data again when the server comes back, however long it was gone', async () => {
    const hub = new DashboardHub({ resolver });
    const [widget] = openWidgets(hub, 1);
    await vi.advanceTimersByTimeAsync(10);
    live().publish(1);
    expect(widget.values).toEqual([1]);

    // The network goes, and stays gone for five minutes: far past graphql-ws's 5 attempts.
    FakeSocket.down = true;
    live().drop(1006);
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(FakeSocket.all.length).toBeGreaterThan(10);
    // Down, and saying so: the badge is the honest signal while it is retried.
    expect(hub.connectionState).toBe('offline');
    expect(widget.errors).toEqual([]);

    FakeSocket.down = false;
    await vi.advanceTimersByTimeAsync(30_000);
    live().publish(2);
    expect(widget.values).toEqual([1, 2]);
    expect(widget.errors).toEqual([]);
    expect(hub.connectionState).toBe('live');
  });

  it('reconnects every stream over one socket per attempt, spaced by a capped, jittered back-off', async () => {
    const hub = new DashboardHub({ resolver });
    const widgets = openWidgets(hub, 40);
    await vi.advanceTimersByTimeAsync(10);
    expect(live().subscriptions).toHaveLength(40);

    FakeSocket.down = true;
    live().drop(1006);
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    // Never more than one connection in flight, however many streams wait on it.
    expect(FakeSocket.peak).toBe(1);
    const attempts = FakeSocket.all.slice(1);
    const gaps = attempts.slice(1).map((s, i) => s.at - attempts[i].at);
    // Ten minutes at no more than one attempt per 15 s once the ceiling is reached.
    expect(attempts.length).toBeLessThanOrEqual(10 * 4 + 5);
    expect(attempts.length).toBeGreaterThanOrEqual(10 * 2);
    for (const gap of gaps) expect(gap).toBeLessThanOrEqual(30_000);
    // At the ceiling every wait lies in its upper half, and they are not all the same.
    const capped = gaps.slice(6);
    for (const gap of capped) expect(gap).toBeGreaterThanOrEqual(15_000);
    expect(new Set(capped).size).toBeGreaterThan(1);

    FakeSocket.down = false;
    const before = FakeSocket.all.length;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(FakeSocket.all.length).toBe(before + 1);
    const socket = live();
    expect(socket.subscriptions).toHaveLength(40);
    socket.publish(7);
    for (const w of widgets) {
      expect(w.values).toEqual([7]);
      expect(w.errors).toEqual([]);
    }
  });

  it('a refused token (4401 at connect) ends the stream instead of retrying it', async () => {
    const hub = new DashboardHub({ resolver });
    FakeSocket.refuseInit = true;
    const [widget] = openWidgets(hub, 1);
    // Timer by timer first, so a retry with no wait at all fails here rather than spinning.
    for (let i = 0; i < 50; i++) await vi.advanceTimersToNextTimerAsync();
    expect(FakeSocket.all).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(FakeSocket.all).toHaveLength(1);
    expect(widget.errors).toHaveLength(1);
    expect((widget.errors[0] as { code: number }).code).toBe(4401);
  });
});
