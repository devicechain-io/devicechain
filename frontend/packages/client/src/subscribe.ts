// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// GraphQL subscriptions over the graphql-transport-ws protocol (ADR-037) — the
// live half of the client wire, consumed by dashboards (the ADR-039 Hub) for
// live telemetry. The backend serves subscriptions on the same /api/<area>/graphql
// path via a WebSocket upgrade; a client derives the ws:// URL from the http one.
//
// This is the low-level SDK primitive: one lazily-created graphql-ws client per
// area (connection reuse), and a subscribe() that binds a typed document to a
// sink. The dashboard Hub layers subscription multiplexing on top (many widgets,
// one connection).
//
// The server accepts only SUBSCRIPTION operations on this socket — queries and
// mutations go over HTTP — and it closes the socket with 4401 when the access token
// it authenticated with expires. subscribe() handles that close itself: when a
// socket that had been acknowledged is closed with 4401, it re-issues the operation
// once, which opens a new socket whose connection_init carries a freshly resolved
// token, and reports the reconnect to the sink as connected(true) so a consumer
// re-queries whatever it may have missed. graphql-ws treats 4401 as fatal and does
// not retry it on its own.

import type { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
import { createClient, type Client } from 'graphql-ws';

import { areaPath, resolveAuthToken, type Area } from './transport';

// The graphql-transport-ws close code for an unauthorized connection. Mid-session
// it means the token the socket authenticated with has expired.
const CLOSE_UNAUTHORIZED = 4401;

// wsUrl builds the absolute ws(s):// URL for an area from the current origin,
// mirroring the relative http path so dev proxy and prod ingress both work.
function wsUrl(area: Area): string {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${window.location.host}${areaPath(area)}`;
}

// An area's shared graphql-ws client, plus what subscribe() needs to know about its
// socket to decide whether a 4401 close is worth one re-subscribe.
interface AreaClient {
  client: Client;
  // The current socket has received connection_ack.
  acked: boolean;
  // The most recently closed socket had been acknowledged. A 4401 on an
  // acknowledged socket is an expiry mid-session, which a fresh token cures; a 4401
  // on one that was never acknowledged is the server refusing the token presented
  // at connect, which re-subscribing would only repeat.
  lastCloseWasAcked: boolean;
}

// One graphql-ws client per area — the WebSocket (and its connection_init auth)
// is reused across every subscription to that area. connectionParams is a
// function so the token is re-resolved on each (re)connect, surviving refresh.
const clients = new Map<Area, AreaClient>();

function clientFor(area: Area): AreaClient {
  let entry = clients.get(area);
  if (!entry) {
    const client = createClient({
      url: wsUrl(area),
      connectionParams: async () => {
        const token = await resolveAuthToken();
        return token ? { Authorization: `Bearer ${token}` } : {};
      },
    });
    const state: AreaClient = { client, acked: false, lastCloseWasAcked: false };
    // Registered once, here, before any subscription can register its own — so on a
    // close this runs first and every sink's error handler (which graphql-ws delivers
    // later, through a rejected promise) reads the same lastCloseWasAcked. That holds
    // even after the first of them re-subscribes and a new socket starts connecting.
    client.on('connecting', () => {
      state.acked = false;
    });
    client.on('connected', () => {
      state.acked = true;
    });
    client.on('closed', () => {
      state.lastCloseWasAcked = state.acked;
      state.acked = false;
    });
    entry = state;
    clients.set(area, entry);
  }
  return entry;
}

// isUnauthorizedClose reports whether a graphql-ws sink error is a 4401 close event.
function isUnauthorizedClose(err: unknown): boolean {
  return (
    typeof err === 'object' && err !== null && 'code' in err && (err as { code: unknown }).code === CLOSE_UNAUTHORIZED
  );
}

export interface SubscriptionSink<T> {
  next: (data: T) => void;
  error?: (err: unknown) => void;
  complete?: () => void;
  // Connection-level signals for the shared per-area socket, distinct from the
  // per-operation next/error/complete above. `connected` fires on each successful
  // connection_ack — `wasRetry` distinguishes a reconnect (after a dropped socket,
  // or after the server closed an expired session) from the first connect; `closed`
  // fires on each socket close (graphql-ws then auto-retries a transient close). A
  // consumer that only needs data can omit both; a live-status indicator uses them
  // to tell "connected but idle" from "offline", and a consumer that must not miss
  // events re-queries on connected(true).
  connected?: (wasRetry: boolean) => void;
  closed?: () => void;
}

// subscribe binds a typed subscription document to a sink and returns an
// unsubscribe function. The document is the same code-generated typed document
// the query path uses, so the result type is inferred; it must be a subscription,
// since the server refuses anything else on this socket. Errors from the operation
// (or the socket) surface via sink.error; normal termination via sink.complete. A
// 4401 close of an acknowledged socket — the server ending an expired session — is
// not surfaced: the operation is re-issued once on a new socket with a fresh token.
export function subscribe<TResult, TVariables>(
  area: Area,
  document: DocumentTypeDecoration<TResult, TVariables> & { toString(): string },
  variables: TVariables extends Record<string, never> ? undefined : TVariables,
  sink: SubscriptionSink<TResult>,
): () => void {
  const entry = clientFor(area);
  const { client } = entry;
  // Set when this operation is re-issued after an expiry, so the next connection_ack
  // reaches the sink as a reconnect: graphql-ws itself reports it as a first connect,
  // because a fatal close ends its retry state.
  let reauth = false;
  // Client-level connection events are registered per subscribe() and torn down with
  // it, alongside the operation itself, so a single dispose() call releases both.
  const disposers: Array<() => void> = [];
  if (sink.connected) {
    disposers.push(
      client.on('connected', (_socket, _payload, wasRetry) => {
        const retried = wasRetry || reauth;
        reauth = false;
        sink.connected!(retried);
      }),
    );
  }
  if (sink.closed) {
    disposers.push(client.on('closed', () => sink.closed!()));
  }

  const payload = {
    query: document.toString(),
    variables: variables as Record<string, unknown> | undefined,
  };
  let disposed = false;
  let current: (() => void) | null = null;
  const start = (): void => {
    const previous = current;
    current = client.subscribe<TResult>(payload, {
      next: (result) => {
        if (result.data != null) sink.next(result.data);
      },
      error: (err) => {
        if (disposed) return;
        if (isUnauthorizedClose(err) && entry.lastCloseWasAcked) {
          reauth = true;
          start();
          return;
        }
        sink.error?.(err);
      },
      complete: () => sink.complete?.(),
    });
    // Release the ended operation only once its replacement holds the client, so the
    // client's count of live operations never touches zero in between (graphql-ws
    // does not release an operation that ended in a fatal close by itself).
    previous?.();
  };
  start();
  disposers.push(() => {
    disposed = true;
    current?.();
  });
  return () => {
    for (const dispose of disposers) dispose();
  };
}

// disposeSubscriptions tears down every cached area client (their sockets). A
// host calls this on sign-out so a stale token's socket does not linger.
export function disposeSubscriptions(): void {
  for (const { client } of clients.values()) {
    void client.dispose();
  }
  clients.clear();
}
