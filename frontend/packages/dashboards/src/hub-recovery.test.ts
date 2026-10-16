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
  // Every subscribe frame any socket has received.
  static subscribes = 0;

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
      FakeSocket.subscribes++;
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

  // The server ends one operation on a socket that stays up, as event-management does when the
  // stream's live source cannot be opened: the resolver's error arrives as a `next` carrying
  // only `errors`, and then `complete`.
  end(sub: Frame): void {
    this.subscriptions = this.subscriptions.filter((s) => s !== sub);
    this.deliver({ id: sub.id, type: 'next', payload: { errors: [{ message: 'live source unavailable' }] } });
    this.deliver({ id: sub.id, type: 'complete' });
  }

  // As a browser does: the close event follows, asynchronously.
  close(code = 1000): void {
    if (this.readyState === FakeSocket.CLOSED) return;
    this.readyState = FakeSocket.CLOSED;
    setTimeout(() => this.onclose?.({ code, reason: '', wasClean: true }), 0);
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
  FakeSocket.subscribes = 0;
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

// The socket stays up, but the server ends one stream: event-management could not open its live
// source (its broker connection was still coming back) or the source closed. The server says so
// with an error-only `next` and a `complete`, not an `error`, so the stream used to sit there,
// subscribed to nothing, under a badge that read `live`.
describe('a stream the server ends while the socket stays up', () => {
  it('is opened again and gets data, without the widget ever showing an error', async () => {
    const hub = new DashboardHub({ resolver });
    const [widget] = openWidgets(hub, 1);
    await vi.advanceTimersByTimeAsync(10);
    const socket = live();
    socket.end(socket.subscriptions[0]);
    await vi.advanceTimersByTimeAsync(1_100);
    expect(FakeSocket.subscribes).toBe(2);
    live().publish(5);
    expect(widget.values).toEqual([5]);
    expect(widget.errors).toEqual([]);
    expect(hub.connectionState).toBe('live');
  });

  it('is opened again after a doubling wait, which starts over once a reading arrives', async () => {
    const hub = new DashboardHub({ resolver });
    openWidgets(hub, 2); // a second stream keeps the socket open throughout
    await vi.advanceTimersByTimeAsync(10);
    const endD0 = (): void => {
      const socket = live();
      socket.end(socket.subscriptions.find((f) => (f.payload as { variables: { deviceToken: string } }).variables.deviceToken === 'd0')!);
    };
    // Re-opened 1 s, then 2 s, then 4 s after each end.
    for (const wait of [1_000, 2_000, 4_000]) {
      endD0();
      const before = FakeSocket.subscribes;
      await vi.advanceTimersByTimeAsync(wait - 50);
      expect(FakeSocket.subscribes).toBe(before);
      await vi.advanceTimersByTimeAsync(100);
      expect(FakeSocket.subscribes).toBe(before + 1);
    }
    // Capped at 30 s however often it ends.
    for (let i = 0; i < 6; i++) {
      endD0();
      await vi.advanceTimersByTimeAsync(30_050);
    }
    const capped = FakeSocket.subscribes;
    endD0();
    await vi.advanceTimersByTimeAsync(30_050);
    expect(FakeSocket.subscribes).toBe(capped + 1);
    // A reading arrives: the next end is re-opened after 1 s again.
    live().publish(1);
    endD0();
    const before = FakeSocket.subscribes;
    await vi.advanceTimersByTimeAsync(1_050);
    expect(FakeSocket.subscribes).toBe(before + 1);
  });

  it('is never re-opened once its widget has gone, waiting or not', async () => {
    const hub = new DashboardHub({ resolver });
    const watch = (deviceToken: string) => hub.subscribeWidget({ kind: 'device', deviceToken, measurements: ['t'] }, { next: () => {} });
    watch('keep'); // keeps the socket open throughout
    const leaves = watch('leaves');
    const ended = watch('ended');
    await vi.advanceTimersByTimeAsync(10);
    const opened = FakeSocket.subscribes;
    // A widget goes while its stream is open: its own unsubscribe completes, and that is not the server.
    leaves();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeSocket.subscribes).toBe(opened);
    // A widget goes while its ended stream waits to be re-opened.
    const socket = live();
    socket.end(socket.subscriptions.find((f) => (f.payload as { variables: { deviceToken: string } }).variables.deviceToken === 'ended')!);
    await vi.advanceTimersByTimeAsync(500);
    ended();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeSocket.subscribes).toBe(opened);
  });

  it('is not re-opened by a sign-out, which ends every stream from the client side', async () => {
    const hub = new DashboardHub({ resolver });
    openWidgets(hub, 1);
    await vi.advanceTimersByTimeAsync(10);
    disposeSubscriptions();
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(FakeSocket.all).toHaveLength(1);
    expect(FakeSocket.subscribes).toBe(1);
  });
});

describe('a board that reads more than one area', () => {
  it('gets every area back after an outage', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ data: { alarms: { results: [], pagination: { total: 0 } } } }))));
    const hub = new DashboardHub({ resolver });
    const [widget] = openWidgets(hub, 1);
    hub.subscribeAlarms({ pageSize: 10 }, { next: () => {}, error: () => {} });
    await vi.advanceTimersByTimeAsync(10);
    const open = () => FakeSocket.all.filter((s) => s.readyState === FakeSocket.OPEN);
    expect(open().map((s) => s.url).sort()).toEqual(['ws://localhost/api/device-management/graphql', 'ws://localhost/api/event-management/graphql'].sort());

    FakeSocket.down = true;
    for (const s of open()) s.drop(1006);
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(hub.connectionState).toBe('offline');

    FakeSocket.down = false;
    await vi.advanceTimersByTimeAsync(30_000);
    const back = open();
    expect(back).toHaveLength(2);
    for (const s of back) expect(s.subscriptions).toHaveLength(1);
    back.find((s) => s.url.includes('event-management'))!.publish(3);
    expect(widget.values).toEqual([3]);
    expect(hub.connectionState).toBe('live');
  });
});

// A stream that resumes has missed whatever was sent while it was down, and nothing will ever
// deliver it. The hub says so, per widget, so a widget that draws history reads it back instead of
// joining the readings either side of the outage with a straight line.
describe('a widget whose stream resumes after an interruption', () => {
  function watch(hub: DashboardHub, deviceToken: string) {
    const w = { values: [] as number[], resumed: 0 };
    const dispose = hub.subscribeWidget(
      { kind: 'device', deviceToken, measurements: ['t'] },
      { next: (s) => w.values.push(s.value as number), reconnected: () => w.resumed++ },
    );
    return { w, dispose };
  }

  it('is told once per reconnect of its socket, and not on the first connect', async () => {
    const hub = new DashboardHub({ resolver });
    const { w } = watch(hub, 'd0');
    await vi.advanceTimersByTimeAsync(10);
    expect(w.resumed).toBe(0);

    FakeSocket.down = true;
    live().drop(1006);
    await vi.advanceTimersByTimeAsync(2 * 60_000);
    expect(w.resumed).toBe(0);
    FakeSocket.down = false;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(w.resumed).toBe(1);

    live().drop(1006);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(w.resumed).toBe(2);
  });

  it('is told when the server ended its stream and it was opened again', async () => {
    const hub = new DashboardHub({ resolver });
    const { w } = watch(hub, 'd0');
    const other = watch(hub, 'd1');
    await vi.advanceTimersByTimeAsync(10);
    const socket = live();
    socket.end(socket.subscriptions.find((f) => (f.payload as { variables: { deviceToken: string } }).variables.deviceToken === 'd0')!);
    await vi.advanceTimersByTimeAsync(500);
    expect(w.resumed).toBe(0);
    await vi.advanceTimersByTimeAsync(600);
    expect(w.resumed).toBe(1);
    // Only the stream that was ended resumed.
    expect(other.w.resumed).toBe(0);
  });

  it('is not told once it has gone', async () => {
    const hub = new DashboardHub({ resolver });
    const { w, dispose } = watch(hub, 'd0');
    watch(hub, 'd1'); // keeps the stream's socket wanted
    await vi.advanceTimersByTimeAsync(10);
    dispose();
    live().drop(1006);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(w.resumed).toBe(0);
  });

  it('every member of a fleet widget is told, through the member feed', async () => {
    const hub = new DashboardHub({ resolver: { ...resolver, devicesForAnchor: async () => ['m1', 'm2'] } });
    let resumed = 0;
    hub.subscribeMembers(
      { kind: 'anchor', anchor: { kind: 'area', token: 'a' }, measurements: ['t'] } as never,
      { roster: () => {}, next: () => {}, reconnected: () => resumed++ },
    );
    await vi.advanceTimersByTimeAsync(10);
    live().drop(1006);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(resumed).toBe(2);
  });
});
