// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A tiny GraphQL-over-fetch client, in the spirit of the REST `fetchJson`
// helper — no Apollo. Each DeviceChain functional area serves its own /graphql
// endpoint; `area` selects which one. Requests carry the access-token Bearer
// when one is available (see setAuthTokenGetter); login/refresh run with no
// token (the GraphQL handler lets an absent token through with no tenant).

import type { DocumentTypeDecoration } from '@graphql-typed-document-node/core';

// DeviceChain functional areas that expose a GraphQL endpoint. The
// `user-management/admin` "area" is the instance-scoped admin API (ADR-033),
// served by user-management at a second path and authenticated with the identity
// token rather than a tenant access token (see the `identity` request option).
export type Area =
  | 'user-management'
  | 'user-management/admin'
  | 'device-management'
  | 'event-management'
  | 'device-state'
  | 'command-delivery';

function endpoint(area: Area): string {
  // Relative URL matching the cluster ingress contract: the ingress routes
  // https://<host>/api/<area>/graphql to each functional-area service (see
  // deploy/helm/devicechain/templates/ingress.yaml), and serves the SPA at "/".
  // In `vite dev` the same path is handled by the proxy in vite.config.ts.
  // 'user-management/admin' resolves to /api/user-management/admin/graphql.
  return `/api/${area}/graphql`;
}

// ── Auth token injection ────────────────────────────────────────────────
//
// The AuthProvider registers a getter so the client can attach a fresh access
// token (refreshing it first if near expiry) without importing React state.

let tokenGetter: (() => Promise<string | null>) | null = null;

export function setAuthTokenGetter(getter: (() => Promise<string | null>) | null) {
  tokenGetter = getter;
}

// The identity-token getter feeds the admin API (ADR-033): admin requests carry
// the instance-scoped identity token, not the tenant access token. Registered by
// the AuthProvider alongside setAuthTokenGetter; selected per request via the
// `identity` option.
let identityTokenGetter: (() => Promise<string | null>) | null = null;

export function setIdentityTokenGetter(getter: (() => Promise<string | null>) | null) {
  identityTokenGetter = getter;
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

interface RequestOptions {
  /** Skip attaching the Bearer token (login / refresh run unauthenticated). */
  anonymous?: boolean;
  /**
   * Authenticate with the identity token instead of the tenant access token —
   * for the instance-scoped admin API (ADR-033). Ignored when `anonymous`.
   */
  identity?: boolean;
}

// `document` is a code-generated typed document (a `TypedDocumentString` from
// the GraphQL Code Generator client preset). It implements
// `DocumentTypeDecoration<TResult, TVariables>`, which carries the result and
// variable types as phantom generics, and stringifies (via `toString()`) to the
// raw query text the server expects. The structural `& { toString(): string }`
// keeps this accepting a document from ANY service while still inferring both
// the result (`TResult`) and the variables (`TVariables`).
export async function gql<TResult, TVariables>(
  area: Area,
  document: DocumentTypeDecoration<TResult, TVariables> & { toString(): string },
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
    res = await fetch(endpoint(area), {
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