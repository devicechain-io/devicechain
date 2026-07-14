// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A tiny GraphQL-over-fetch client, in the spirit of a REST `fetchJson` helper —
// no Apollo. Each DeviceChain functional area serves its own /graphql endpoint;
// `area` selects which one. Requests carry the access-token Bearer when one is
// available (see setAuthTokenGetter); login/refresh run with no token (the
// GraphQL handler lets an absent token through with no tenant).

import type { DocumentTypeDecoration } from '@graphql-typed-document-node/core';

// DeviceChain functional areas that expose a GraphQL endpoint. The
// `user-management/admin` "area" is the instance-scoped admin API (ADR-033),
// served by user-management at a second path and authenticated with the identity
// token rather than a tenant access token (see the `identity` request option).
export type Area =
  | 'user-management'
  | 'user-management/admin'
  | 'user-management/settings'
  | 'device-management'
  | 'event-management'
  | 'event-processing'
  | 'device-state'
  | 'command-delivery'
  | 'dashboard-management'
  | 'outbound-connectors';

// Relative URL matching the cluster ingress contract: the ingress routes
// https://<host>/api/<area>/graphql to each functional-area service and serves
// the SPA at "/". In `vite dev` the same path is handled by the config proxy.
// 'user-management/admin' resolves to /api/user-management/admin/graphql.
export function areaPath(area: Area): string {
  return `/api/${area}/graphql`;
}

// ── Auth token injection ────────────────────────────────────────────────
//
// A host (e.g. the console's AuthProvider) registers a getter so the client can
// attach a fresh access token — refreshing it first if near expiry — without the
// SDK owning React state or storage.

let tokenGetter: (() => Promise<string | null>) | null = null;

export function setAuthTokenGetter(getter: (() => Promise<string | null>) | null) {
  tokenGetter = getter;
}

// The identity-token getter feeds the admin API (ADR-033): admin requests carry
// the instance-scoped identity token, not the tenant access token. Registered
// alongside setAuthTokenGetter; selected per request via the `identity` option.
let identityTokenGetter: (() => Promise<string | null>) | null = null;

export function setIdentityTokenGetter(getter: (() => Promise<string | null>) | null) {
  identityTokenGetter = getter;
}

// resolveAuthToken returns the current access token (refreshing via the getter),
// or null. Internal to the SDK — the ws subscribe path (subscribe.ts) reuses it
// so subscriptions authenticate with the same token as queries.
export async function resolveAuthToken(): Promise<string | null> {
  return tokenGetter ? tokenGetter() : null;
}

// ── Errors ──────────────────────────────────────────────────────────────

export class GraphQLRequestError extends Error {
  constructor(
    message: string,
    /** HTTP status, or 0 for a transport-level failure. */
    public status: number,
    /** The raw `errors` array from a GraphQL response, when present. */
    public errors?: { message: string }[],
  ) {
    super(message);
    this.name = 'GraphQLRequestError';
  }
}

interface GraphQLResponse<T> {
  data?: T;
  errors?: { message: string }[];
}

export interface RequestOptions {
  /** Skip attaching the Bearer token (login / refresh run unauthenticated). */
  anonymous?: boolean;
  /**
   * Authenticate with the identity token instead of the tenant access token —
   * for the instance-scoped admin API (ADR-033). Ignored when `anonymous`.
   */
  identity?: boolean;
}

// A GraphQL document tagged with its result/variable types — what gql()/subscribe()
// accept. A code-generated `TypedDocumentString` (client preset) satisfies it, and
// so does a hand-authored query string cast to it: the SDK runs in documentMode
// 'string', calling only `toString()` to get the query text, so the phantom result
// (`TResult`) and variable (`TVariables`) generics are all that distinguish them.
// Exported so packages/apps without codegen (dashboards, widgets, the dash app)
// hand-author typed documents against one shared alias instead of re-inlining it.
export type TypedDocument<TResult, TVariables> = DocumentTypeDecoration<TResult, TVariables> & {
  toString(): string;
};

// `document` is a code-generated typed document (a `TypedDocumentString` from the
// GraphQL Code Generator client preset). It implements
// `DocumentTypeDecoration<TResult, TVariables>`, which carries the result and
// variable types as phantom generics, and stringifies (via `toString()`) to the
// raw query text the server expects. The structural `& { toString(): string }`
// keeps this accepting a document from ANY service while still inferring both the
// result (`TResult`) and the variables (`TVariables`).
export async function gql<TResult, TVariables>(
  area: Area,
  document: TypedDocument<TResult, TVariables>,
  ...[variables, options]: TVariables extends Record<string, never>
    ? [variables?: undefined, options?: RequestOptions]
    : [variables: TVariables, options?: RequestOptions]
): Promise<TResult> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };

  if (!options?.anonymous) {
    const getter = options?.identity ? identityTokenGetter : tokenGetter;
    if (getter) {
      const token = await getter();
      if (token) headers['Authorization'] = `Bearer ${token}`;
    }
  }

  let res: Response;
  try {
    res = await fetch(areaPath(area), {
      method: 'POST',
      headers,
      body: JSON.stringify({ query: document.toString(), variables }),
    });
  } catch (err) {
    throw new GraphQLRequestError(
      err instanceof Error ? err.message : 'Network request failed',
      0,
    );
  }

  if (!res.ok) {
    throw new GraphQLRequestError(`Request failed (${res.status})`, res.status);
  }

  const body = (await res.json()) as GraphQLResponse<TResult>;
  if (body.errors && body.errors.length > 0) {
    throw new GraphQLRequestError(body.errors[0].message, res.status, body.errors);
  }
  if (body.data === undefined) {
    throw new GraphQLRequestError('Empty GraphQL response', res.status);
  }
  return body.data;
}
