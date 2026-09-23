// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// subscribe()'s re-subscribe on an expired session, against a scripted graphql-ws
// client. These pin the decision logic edge by edge; subscribe.reauth.test.ts drives
// the same path through the real graphql-ws over a fake socket, which is what would
// notice graphql-ws changing the order of its events underneath this logic.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type Listener = (...args: unknown[]) => void;

interface FakeSink {
  next: (v: unknown) => void;
  error: (e: unknown) => void;
  complete: () => void;
}

interface FakeOp {
  payload: { query: string; variables?: Record<string, unknown> };
  sink: FakeSink;
  disposed: number;
}

// A scripted stand-in for a graphql-ws Client: it records every subscribe and lets
// the test emit client events in the order graphql-ws does — 'closed' synchronously,
// then each sink's error on a later microtask.
class FakeClient {
  listeners: Record<string, Listener[]> = {};
  ops: FakeOp[] = [];

  on(event: string, l: Listener): () => void {
    (this.listeners[event] ??= []).push(l);
    return () => {
      this.listeners[event] = this.listeners[event].filter((x) => x !== l);
    };
  }

  emit(event: string, ...args: unknown[]): void {
    for (const l of [...(this.listeners[event] ?? [])]) l(...args);
  }

  subscribe(payload: FakeOp['payload'], sink: FakeSink): () => void {
    const op: FakeOp = { payload, sink, disposed: 0 };
    this.ops.push(op);
    return () => {
      op.disposed++;
    };
  }

  connect(retry = false): void {
    this.emit('connecting', retry);
    this.emit('connected', {}, undefined, retry);
  }

  // A socket close as graphql-ws reports it: 'closed' now, each live sink's error
  // afterwards, through a promise.
  async close(code: number, live: FakeOp[]): Promise<void> {
    const event = { code, reason: '' };
    this.emit('closed', event);
    await Promise.resolve();
    for (const op of live) op.sink.error(event);
    await Promise.resolve();
  }

  dispose(): void {}
}

let fake: FakeClient;

vi.mock('graphql-ws', () => ({
  createClient: () => fake,
}));

const doc = { toString: () => 'subscription { ticks }' } as never;

async function load() {
  vi.resetModules();
  return import('./subscribe');
}

beforeEach(() => {
  fake = new FakeClient();
  vi.stubGlobal('window', { location: { protocol: 'http:', host: 'localhost' } });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('subscribe on a 4401 close', () => {
  it('re-subscribes after an acknowledged socket expires, and reports it as a reconnect', async () => {
    const { subscribe } = await load();
    const connected: boolean[] = [];
    const errors: unknown[] = [];
    subscribe('event-management', doc, undefined as never, {
      next: () => {},
      error: (e) => errors.push(e),
      connected: (r) => connected.push(r),
    });
    fake.connect();
    expect(fake.ops).toHaveLength(1);

    await fake.close(4401, [fake.ops[0]]);
    expect(errors).toEqual([]);
    expect(fake.ops).toHaveLength(2);
    expect(fake.ops[1].payload).toEqual(fake.ops[0].payload);
    expect(fake.ops[0].disposed).toBe(1);

    fake.connect(false); // graphql-ws reports a fresh connect after a fatal close
    expect(connected).toEqual([false, true]);
  });

  it('surfaces a 4401 at connect, and does not retry it', async () => {
    const { subscribe } = await load();
    const errors: unknown[] = [];
    subscribe('event-management', doc, undefined as never, { next: () => {}, error: (e) => errors.push(e) });
    fake.emit('connecting', false); // never acknowledged

    await fake.close(4401, [fake.ops[0]]);
    expect(errors).toEqual([{ code: 4401, reason: '' }]);
    expect(fake.ops).toHaveLength(1);
  });

  it('retries at most once per acknowledged session', async () => {
    const { subscribe } = await load();
    const errors: unknown[] = [];
    subscribe('event-management', doc, undefined as never, { next: () => {}, error: (e) => errors.push(e) });
    fake.connect();
    await fake.close(4401, [fake.ops[0]]);
    expect(fake.ops).toHaveLength(2);

    // The new socket is refused at connect (the token could not be refreshed).
    fake.emit('connecting', false);
    await fake.close(4401, [fake.ops[1]]);
    expect(errors).toEqual([{ code: 4401, reason: '' }]);
    expect(fake.ops).toHaveLength(2);
  });

  it('re-subscribes every operation that shared the expired socket', async () => {
    const { subscribe } = await load();
    const connectedA: boolean[] = [];
    const connectedB: boolean[] = [];
    subscribe('event-management', doc, undefined as never, { next: () => {}, connected: (r) => connectedA.push(r) });
    subscribe('event-management', doc, undefined as never, { next: () => {}, connected: (r) => connectedB.push(r) });
    fake.connect();

    await fake.close(4401, [fake.ops[0], fake.ops[1]]);
    expect(fake.ops).toHaveLength(4);
    fake.connect(false);
    expect(connectedA).toEqual([false, true]);
    expect(connectedB).toEqual([false, true]);
  });

  it('surfaces any other close code', async () => {
    const { subscribe } = await load();
    const errors: unknown[] = [];
    subscribe('event-management', doc, undefined as never, { next: () => {}, error: (e) => errors.push(e) });
    fake.connect();

    await fake.close(4400, [fake.ops[0]]);
    expect(errors).toEqual([{ code: 4400, reason: '' }]);
    expect(fake.ops).toHaveLength(1);
  });

  it('releases the re-issued operation, and issues nothing more, once disposed', async () => {
    const { subscribe } = await load();
    const dispose = subscribe('event-management', doc, undefined as never, { next: () => {} });
    fake.connect();
    await fake.close(4401, [fake.ops[0]]);
    expect(fake.ops).toHaveLength(2);

    dispose();
    expect(fake.ops[1].disposed).toBe(1);
    // A late error for the released operation must not start another.
    fake.ops[1].sink.error({ code: 4401, reason: '' });
    expect(fake.ops).toHaveLength(2);
  });
});
