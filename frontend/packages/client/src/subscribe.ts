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

import type { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
import { createClient, type Client } from 'graphql-ws';

import { areaPath, resolveAuthToken, type Area } from './transport';

// wsUrl builds the absolute ws(s):// URL for an area from the current origin,
// mirroring the relative http path so dev proxy and prod ingress both work.
function wsUrl(area: Area): string {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${window.location.host}${areaPath(area)}`;
}

// One graphql-ws client per area — the WebSocket (and its connection_init auth)
// is reused across every subscription to that area. connectionParams is a
// function so the token is re-resolved on each (re)connect, surviving refresh.
const clients = new Map<Area, Client>();

function clientFor(area: Area): Client {
  let client = clients.get(area);
  if (!client) {
    client = createClient({
      url: wsUrl(area),
      connectionParams: async () => {
        const token = await resolveAuthToken();
        return token ? { Authorization: `Bearer ${token}` } : {};
      },
    });
    clients.set(area, client);
  }
  return client;
}

export interface SubscriptionSink<T> {
  next: (data: T) => void;
  error?: (err: unknown) => void;
  complete?: () => void;
  // Connection-level signals for the shared per-area socket, distinct from the
  // per-operation next/error/complete above. `connected` fires on each successful
  // connection_ack — `wasRetry` distinguishes a reconnect (after a dropped socket)
  // from the first connect; `closed` fires on each socket close (graphql-ws then
  // auto-retries a transient close). A consumer that only needs data can omit both;
  // a live-status indicator uses them to tell "connected but idle" from "offline".
  connected?: (wasRetry: boolean) => void;
  closed?: () => void;
}

// subscribe binds a typed subscription document to a sink and returns an
// unsubscribe function. The document is the same code-generated typed document
// the query path uses, so the result type is inferred. Errors from the operation
// (or the socket) surface via sink.error; normal termination via sink.complete.
export function subscribe<TResult, TVariables>(
  area: Area,
  document: DocumentTypeDecoration<TResult, TVariables> & { toString(): string },
  variables: TVariables extends Record<string, never> ? undefined : TVariables,
  sink: SubscriptionSink<TResult>,
): () => void {
  const client = clientFor(area);
  // Client-level connection events are registered per subscribe() and torn down with
  // it, alongside the operation itself, so a single dispose() call releases both.
  const disposers: Array<() => void> = [];
  if (sink.connected) {
    disposers.push(client.on('connected', (_socket, _payload, wasRetry) => sink.connected!(wasRetry)));
  }
  if (sink.closed) {
    disposers.push(client.on('closed', () => sink.closed!()));
  }
  disposers.push(
    client.subscribe<TResult>(
      {
        query: document.toString(),
        variables: variables as Record<string, unknown> | undefined,
      },
      {
        next: (result) => {
          if (result.data != null) sink.next(result.data);
        },
        error: (err) => sink.error?.(err),
        complete: () => sink.complete?.(),
      },
    ),
  );
  return () => {
    for (const dispose of disposers) dispose();
  };
}

// disposeSubscriptions tears down every cached area client (their sockets). A
// host calls this on sign-out so a stale token's socket does not linger.
export function disposeSubscriptions(): void {
  for (const client of clients.values()) {
    void client.dispose();
  }
  clients.clear();
}
