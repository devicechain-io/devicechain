// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A tiny GraphQL-over-fetch client, in the spirit of the REST `fetchJson`
// helper — no Apollo. Each DeviceChain functional area serves its own /graphql
// endpoint; `area` selects which one. Requests carry the access-token Bearer
// when one is available (see setAuthTokenGetter); login/refresh run with no
// token (the GraphQL handler lets an absent token through with no tenant).

// DeviceChain functional areas that expose a GraphQL endpoint.
export type Area =
  | 'user-management'
  | 'device-management'
  | 'event-management'
  | 'device-state'
  | 'command-delivery';

function endpoint(area: Area): string {
  // Mirrors the ingress contract: https://<host>/<area>/graphql. The dev proxy
  // (vite.config.ts) forwards /api/<area>/... to a backend.
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
}

export async function gql<T>(
  area: Area,
  query: string,
  variables?: Record<string, unknown>,
  options?: RequestOptions,
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };

  if (!options?.anonymous && tokenGetter) {
    const token = await tokenGetter();
    if (token) headers['Authorization'] = `Bearer ${token}`;
  }

  let res: Response;
  try {
    res = await fetch(endpoint(area), {
      method: 'POST',
      headers,
      body: JSON.stringify({ query, variables }),
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

  const body = (await res.json()) as GraphQLResponse<T>;
  if (body.errors && body.errors.length > 0) {
    throw new GraphQLRequestError(body.errors[0].message, res.status, body.errors);
  }
  if (body.data === undefined) {
    throw new GraphQLRequestError('Empty GraphQL response', res.status);
  }
  return body.data;
}