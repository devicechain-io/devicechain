// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// subscribe()'s re-subscribe on an expired session, through the REAL graphql-ws
// client over a fake WebSocket. The logic depends on graphql-ws emitting 'closed'
// synchronously and delivering each sink's error afterwards; the scripted tests in
// subscribe.test.ts assume that order, and this is the test that would fail if a
// graphql-ws upgrade changed it.

import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { disposeSubscriptions, subscribe } from './subscribe';
import { GraphQLRequestError, setAuthTokenGetter } from './transport';

interface Frame {
  id?: string;
  type: string;
  payload?: Record<string, unknown>;
}

// FakeSocket is a server-side script for one connection, shaped as the browser
// WebSocket graphql-ws drives: it acks connection_init and answers each subscribe
// with one `next`, recording every frame the client sent.
class FakeSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static all: FakeSocket[] = [];
  // When set, connection_init is answered with a 4401 close instead of an ack: the
  // server refusing the token presented at connect.
  static refuseInit = false;

  readyState = FakeSocket.CONNECTING;
  sent: Frame[] = [];
  onopen: (() => void) | null = null;
  onclose: ((e: { code: number; reason: string; wasClean: boolean }) => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;

  constructor(
    public url: string,
    public protocol: string,
  ) {
    FakeSocket.all.push(this);
    setTimeout(() => {
      this.readyState = FakeSocket.OPEN;
      this.onopen?.();
    }, 0);
  }

  send(data: string): void {
    const frame = JSON.parse(data) as Frame;
    this.sent.push(frame);
    if (frame.type === 'connection_init' && FakeSocket.refuseInit) {
      setTimeout(() => this.serverClose(4401, 'invalid or expired token'), 0);
    } else if (frame.type === 'connection_init') {
      setTimeout(() => this.deliver({ type: 'connection_ack' }), 0);
    } else if (frame.type === 'subscribe') {
      setTimeout(() => this.deliver({ id: frame.id, type: 'next', payload: { data: { ticks: FakeSocket.all.length } } }), 0);
    }
  }

  deliver(frame: Frame): void {
    if (this.readyState === FakeSocket.OPEN) this.onmessage?.({ data: JSON.stringify(frame) });
  }

  // The server ending the connection.
  serverClose(code: number, reason: string): void {
    this.readyState = FakeSocket.CLOSED;
    this.onclose?.({ code, reason, wasClean: true });
  }

  close(): void {
    this.readyState = FakeSocket.CLOSED;
  }
}

beforeEach(() => {
  FakeSocket.all = [];
  FakeSocket.refuseInit = false;
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.stubGlobal('window', { location: { protocol: 'http:', host: 'localhost' } });
});

afterEach(() => {
  disposeSubscriptions();
  setAuthTokenGetter(null);
  vi.unstubAllGlobals();
});

it('re-subscribes on a new socket with a fresh token after the server closes an expired session with 4401', async () => {
  const tokens = ['first-token', 'second-token'];
  let calls = 0;
  setAuthTokenGetter(async () => tokens[Math.min(calls++, tokens.length - 1)]);

  const received: unknown[] = [];
  const connected: boolean[] = [];
  const errors: unknown[] = [];
  const doc = { toString: () => 'subscription { ticks }' } as never;
  const dispose = subscribe('event-management', doc, undefined as never, {
    next: (d) => received.push(d),
    error: (e) => errors.push(e),
    connected: (r) => connected.push(r),
  });

  await vi.waitFor(() => expect(received).toEqual([{ ticks: 1 }]));
  const first = FakeSocket.all[0];
  expect(first.url).toBe('ws://localhost/api/event-management/graphql');
  expect(first.sent[0]).toEqual({ type: 'connection_init', payload: { Authorization: 'Bearer first-token' } });

  first.serverClose(4401, 'token expired');

  await vi.waitFor(() => expect(received).toEqual([{ ticks: 1 }, { ticks: 2 }]));
  expect(FakeSocket.all).toHaveLength(2);
  const second = FakeSocket.all[1];
  expect(second.sent[0]).toEqual({ type: 'connection_init', payload: { Authorization: 'Bearer second-token' } });
  expect(second.sent[1]).toMatchObject({ type: 'subscribe', payload: { query: 'subscription { ticks }' } });
  expect(connected).toEqual([false, true]);
  expect(errors).toEqual([]);

  dispose();
});

it('surfaces a 4401 at connect instead of looping', async () => {
  setAuthTokenGetter(async () => 'expired-token');
  FakeSocket.refuseInit = true;
  const errors: Array<{ code?: number }> = [];
  const doc = { toString: () => 'subscription { ticks }' } as never;
  subscribe('event-management', doc, undefined as never, {
    next: () => {},
    error: (e) => errors.push(e as { code?: number }),
  });

  await vi.waitFor(() => expect(errors).toHaveLength(1));
  expect(errors[0].code).toBe(4401);
  await new Promise((r) => setTimeout(r, 20));
  expect(FakeSocket.all).toHaveLength(1);
});

// The session's getAccessToken throws when the held access token has expired and the refresh
// failed transiently (no response, a 5xx, a 429). graphql-ws hands any throw from
// connectionParams to shouldRetry and then closes the socket with 4005; answered false, it ends
// every operation with that error, so an outage of the identity service at the wrong moment
// used to end every subscription.
it('re-subscribes after a connect that lost its token to a transient refresh failure', async () => {
  let calls = 0;
  setAuthTokenGetter(async () => {
    calls++;
    if (calls === 1) throw new GraphQLRequestError('Request failed (503)', 503);
    return 'fresh-token';
  });

  const received: unknown[] = [];
  const connected: boolean[] = [];
  const errors: unknown[] = [];
  const doc = { toString: () => 'subscription { ticks }' } as never;
  const dispose = subscribe('event-management', doc, undefined as never, {
    next: (d) => received.push(d),
    error: (e) => errors.push(e),
    connected: (r) => connected.push(r),
  });

  await vi.waitFor(() => expect(received).toHaveLength(1), { timeout: 3000 });
  expect(errors).toEqual([]);
  expect(calls).toBe(2);
  const last = FakeSocket.all[FakeSocket.all.length - 1];
  expect(last.sent[0]).toEqual({ type: 'connection_init', payload: { Authorization: 'Bearer fresh-token' } });
  // Reported as a reconnect, so a consumer re-queries what it may have missed.
  expect(connected).toEqual([true]);

  dispose();
});

// The negative control: a refresh the server REFUSED (a GraphQL error) is not an outage, and
// a token getter that fails for any other reason is not one either. Both still surface, once.
it.each([
  ['a refused refresh', () => new GraphQLRequestError('invalid refresh token', 200, [{ message: 'invalid refresh token' }])],
  ['an error that is not a transport failure', () => new Error('boom')],
])('surfaces %s instead of retrying it', async (_name, failure) => {
  let calls = 0;
  setAuthTokenGetter(async () => {
    calls++;
    throw failure();
  });
  const errors: unknown[] = [];
  const doc = { toString: () => 'subscription { ticks }' } as never;
  subscribe('event-management', doc, undefined as never, {
    next: () => {},
    error: (e) => errors.push(e),
  });

  await vi.waitFor(() => expect(errors).toHaveLength(1));
  expect((errors[0] as Error).message).toBe(failure().message);
  await new Promise((r) => setTimeout(r, 1500)); // longer than the first back-off
  expect(errors).toHaveLength(1);
  expect(calls).toBe(1);
});

it('stops retrying an unavailable token once the subscription is disposed', async () => {
  let calls = 0;
  setAuthTokenGetter(async () => {
    calls++;
    throw new GraphQLRequestError('Failed to fetch', 0);
  });
  const errors: unknown[] = [];
  const doc = { toString: () => 'subscription { ticks }' } as never;
  const dispose = subscribe('event-management', doc, undefined as never, {
    next: () => {},
    error: (e) => errors.push(e),
  });
  await vi.waitFor(() => expect(calls).toBe(1));
  await new Promise((r) => setTimeout(r, 20)); // the 4005 close has been handled
  dispose();
  await new Promise((r) => setTimeout(r, 1500));
  expect(calls).toBe(1);
  expect(errors).toEqual([]);
});
