// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The request error types, in a module of their own so transport.ts and skew.ts can both
// depend on them without importing each other. Re-exported from transport.ts and index.ts.

export class GraphQLRequestError extends Error {
  constructor(
    message: string,
    /** HTTP status, or 0 for a transport-level failure. */
    public status: number,
    /** The raw `errors` array from a GraphQL response, when present. */
    public errors?: GraphQLErrorEntry[],
  ) {
    super(message);
    this.name = 'GraphQLRequestError';
  }
}

/**
 * One entry of a GraphQL response's `errors` array. `extensions` carries the server's
 * machine-readable detail when it sends any — for example `code: "THROTTLED"` with
 * `retryAfterSeconds` on a sign-in refused by the per-account backoff — so a caller can
 * branch on a code instead of matching the message text.
 */
export interface GraphQLErrorEntry {
  message: string;
  extensions?: Record<string, unknown>;
}
