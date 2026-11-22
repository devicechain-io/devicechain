// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Schema-skew negotiation for the browser client: the TypeScript half of what dcctl does in Go.
//
// A console (or any embedder) built against one release can be pointed at an instance running
// another. The typed documents are fixed at build time, so a field the instance does not serve
// would otherwise reach it and fail as an opaque GraphQL error, or worse, half work. With
// negotiation enabled, each authenticated operation is checked against the SDL the target
// endpoint itself serves BEFORE it is dispatched:
//
//   OPTIONS <endpoint>  Accept: application/vnd.devicechain.schema+json
//     -> 200 { "format": 1, "schema": "<SDL>" }
//
// Nothing is pruned, rewritten or downgraded: an operation the instance cannot serve is refused
// with a typed error naming what is missing. An instance that predates discovery (v1.8 and
// older answer the OPTIONS with 400/404/405/204) is a version this client cannot identify, so it
// fails loudly rather than issuing requests that instance may not serve.
//
// The checking engine (and with it `graphql`) lives in skew-engine.ts and is loaded on first use.

import type { Contract } from './skew-engine';
import { GraphQLRequestError } from './request-error';

/** The media type of the schema-discovery request and response. */
export const SCHEMA_MEDIA_TYPE = 'application/vnd.devicechain.schema+json';

/** The only discovery format this client understands. */
const SUPPORTED_FORMAT = 1;

/** The first instance release that serves schema discovery. */
const DISCOVERY_RELEASE = 'v1.9.0';

/**
 * The HTTP status a refused request carries. Not 0: that means "never reached the network" and
 * callers treat it as the instance being unreachable, which a refusal is not. 422 reads as what it
 * is: the request is well-formed but this instance cannot process it.
 */
const REFUSED_STATUS = 422;

/**
 * Why negotiation refused a request:
 * - `unsupported`: the instance's schema does not define something the operation uses (a field,
 *   argument, type or input field). The usual cause is an instance older than the client.
 * - `invalid-request`: the operation does not validate against the instance's schema for any other
 *   reason. This says nothing about the instance's age.
 * - `undiscoverable`: the instance answers the discovery request in a way that means it predates
 *   discovery, so its schema (and release) cannot be confirmed.
 * - `invalid-contract`: the instance returned a discovery document this client cannot use
 *   (an unknown format, empty, or unparseable schema).
 */
export type SchemaSkewKind = 'unsupported' | 'invalid-request' | 'undiscoverable' | 'invalid-contract';

/**
 * Thrown, before anything is sent, when negotiation is enabled and the operation cannot be
 * confirmed against the connected instance. It extends GraphQLRequestError so existing handlers
 * keep treating it as a failed request; its status is 422 for a refused request and 0 only when
 * the instance's contract could not be read at all.
 */
export class SchemaSkewError extends GraphQLRequestError {
  constructor(
    message: string,
    public kind: SchemaSkewKind,
    /** The area endpoint path the contract belongs to. */
    public endpoint: string,
  ) {
    super(message, kind === 'unsupported' || kind === 'invalid-request' ? REFUSED_STATUS : 0);
    this.name = 'SchemaSkewError';
  }
}

/** True for a SchemaSkewError, across module copies. */
export function isSchemaSkewError(err: unknown): err is SchemaSkewError {
  return err instanceof Error && err.name === 'SchemaSkewError';
}

// Statuses a pre-discovery GraphQL handler (or a proxy in front of one) answers an empty OPTIONS
// with. They are NOT a release number; they only mean no contract was offered.
const NO_DISCOVERY_STATUSES = new Set([204, 400, 404, 405]);

/** A discovery fetch for one endpoint, with auth and 401 recovery already applied. */
export type DiscoveryFetch = () => Promise<Response>;

/** Options for enableSchemaNegotiation. */
export interface SchemaNegotiationOptions {
  /** The release of this client or console, named in refusals (for example `v1.9.0`). */
  release?: string;
}

let enabled = false;
let clientRelease: string | undefined;
const contracts = new Map<string, Promise<Contract>>();

/**
 * Turn schema negotiation on for every authenticated request this client sends. Opt-in, and
 * idempotent. Anonymous exchanges (login, refresh) keep their stable contract and are never
 * checked. Subscriptions are not checked. The `graphql` peer dependency is loaded only when the
 * first operation is checked.
 *
 * Negotiation sends an `OPTIONS` request to each GraphQL endpoint. A proxy that answers `OPTIONS`
 * itself (a CORS preflight handler, a WAF) makes discovery fail, and a failed discovery refuses
 * the operation: negotiation fails closed.
 */
export function enableSchemaNegotiation(options?: SchemaNegotiationOptions): void {
  enabled = true;
  clientRelease = options?.release;
}

/** Turn negotiation off and forget every cached contract. */
export function disableSchemaNegotiation(): void {
  enabled = false;
  clientRelease = undefined;
  contracts.clear();
}

/**
 * Forget the cached contracts so the next operation rediscovers them. Contracts are cached per
 * endpoint for the life of the page, and an operation an instance appears not to serve is
 * re-checked once against a fresh contract before it is refused, so a rolling upgrade heals by
 * itself; call this to force rediscovery sooner (a new sign-in, a changed target). A failed
 * discovery is never cached.
 */
export function resetSchemaNegotiation(): void {
  contracts.clear();
}

export function schemaNegotiationEnabled(): boolean {
  return enabled;
}

async function discover(endpoint: string, fetchContract: DiscoveryFetch): Promise<Contract> {
  const res = await fetchContract();
  if (NO_DISCOVERY_STATUSES.has(res.status)) {
    throw new SchemaSkewError(
      `cannot confirm what this instance serves at ${endpoint}: it does not offer schema discovery ` +
        `(HTTP ${res.status}), so its release is unknown; this console needs an instance running ` +
        `${DISCOVERY_RELEASE} or later`,
      'undiscoverable',
      endpoint,
    );
  }
  if (!res.ok) {
    throw new GraphQLRequestError(`Schema discovery failed (${res.status})`, res.status);
  }
  let manifest: { format?: unknown; schema?: unknown };
  try {
    manifest = (await res.json()) as typeof manifest;
  } catch {
    throw new SchemaSkewError(`${endpoint} returned an unreadable schema contract`, 'invalid-contract', endpoint);
  }
  if (manifest.format !== SUPPORTED_FORMAT || typeof manifest.schema !== 'string' || manifest.schema.trim() === '') {
    throw new SchemaSkewError(
      `${endpoint} returned an unsupported or empty schema contract (format ${String(manifest.format)}); ` +
        'this console is older than the instance: use the console release that matches it',
      'invalid-contract',
      endpoint,
    );
  }
  const { loadContract } = await import('./skew-engine');
  try {
    return loadContract(manifest.schema);
  } catch (err) {
    throw new SchemaSkewError(
      `${endpoint} returned a schema contract this client cannot read: ${err instanceof Error ? err.message : String(err)}`,
      'invalid-contract',
      endpoint,
    );
  }
}

interface Held {
  contract: Promise<Contract>;
  /** True when this call found the contract already cached (it may predate an upgrade). */
  cached: boolean;
}

function contractFor(endpoint: string, fetchContract: DiscoveryFetch): Held {
  const existing = contracts.get(endpoint);
  if (existing) return { contract: existing, cached: true };
  const created = discover(endpoint, fetchContract);
  contracts.set(endpoint, created);
  // A failed or refused discovery is not cached: the next call asks again.
  created.catch(() => {
    if (contracts.get(endpoint) === created) contracts.delete(endpoint);
  });
  return { contract: created, cached: false };
}

/**
 * Check one operation against the endpoint's served contract. Resolves when it can be served,
 * rejects with a SchemaSkewError naming what cannot, or with the discovery failure. Never modifies
 * the document or the variables. A refusal from a contract cached earlier is checked once more
 * against a freshly discovered one before it is raised, so an instance upgraded under a running
 * page is not refused on what it used to serve.
 */
export async function checkOperation(
  endpoint: string,
  fetchContract: DiscoveryFetch,
  query: string,
  variables: unknown,
): Promise<void> {
  const { findProblems } = await import('./skew-engine');
  const first = contractFor(endpoint, fetchContract);
  let held = await first.contract;
  let problems = findProblems(held, query, variables);
  if (problems.length > 0 && first.cached) {
    if (contracts.get(endpoint) === first.contract) contracts.delete(endpoint);
    held = await contractFor(endpoint, fetchContract).contract;
    problems = findProblems(held, query, variables);
  }
  if (problems.length === 0) return;

  const detail = problems.map((p) => p.message).join('; ');
  if (problems.every((p) => p.missing)) {
    const who = clientRelease ? `this console (${clientRelease})` : 'this console';
    throw new SchemaSkewError(
      `this instance does not serve what ${who} asked for: ${detail}; the instance is likely older than the console, so use a console release that matches it`,
      'unsupported',
      endpoint,
    );
  }
  throw new SchemaSkewError(`this request is invalid for this instance: ${detail}`, 'invalid-request', endpoint);
}
