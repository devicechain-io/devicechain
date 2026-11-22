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

import type { GraphQLSchema } from 'graphql';
import { GraphQLRequestError } from './request-error';

/** The media type of the schema-discovery request and response. */
export const SCHEMA_MEDIA_TYPE = 'application/vnd.devicechain.schema+json';

/** The only discovery format this client understands. */
const SUPPORTED_FORMAT = 1;

/** The first instance release that serves schema discovery. */
const DISCOVERY_RELEASE = 'v1.9.0';

/**
 * Why negotiation refused a request:
 * - `unsupported`: the instance's schema does not serve something the operation uses.
 * - `undiscoverable`: the instance answers the discovery request in a way that means it predates
 *   discovery, so its schema (and release) cannot be confirmed.
 * - `invalid-contract`: the instance returned a discovery document this client cannot use
 *   (an unknown format, empty, or unparseable schema).
 */
export type SchemaSkewKind = 'unsupported' | 'undiscoverable' | 'invalid-contract';

/**
 * Thrown, before anything is sent, when negotiation is enabled and the operation cannot be
 * confirmed against the connected instance. It extends GraphQLRequestError (status 0: nothing
 * reached the operation) so existing handlers keep treating it as a failed request.
 */
export class SchemaSkewError extends GraphQLRequestError {
  constructor(
    message: string,
    public kind: SchemaSkewKind,
    /** The area endpoint path the contract belongs to. */
    public endpoint: string,
  ) {
    super(message, 0);
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

interface Contract {
  schema: GraphQLSchema;
}

let enabled = false;
const contracts = new Map<string, Promise<Contract>>();

/**
 * Turn schema negotiation on for every authenticated request this client sends. Opt-in, and
 * idempotent. Anonymous exchanges (login, refresh) keep their stable contract and are never
 * checked. Subscriptions are not checked. The `graphql` peer dependency is loaded only when the
 * first operation is checked.
 */
export function enableSchemaNegotiation(): void {
  enabled = true;
}

/** Turn negotiation off and forget every cached contract. */
export function disableSchemaNegotiation(): void {
  enabled = false;
  contracts.clear();
}

/**
 * Forget the cached contracts so the next operation rediscovers them. Call after the instance may
 * have been upgraded (a new sign-in, a changed target). Contracts are cached per endpoint for the
 * life of the page otherwise, and a failed discovery is never cached.
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
  const { buildSchema } = await import('graphql');
  try {
    return { schema: buildSchema(manifest.schema) };
  } catch (err) {
    throw new SchemaSkewError(
      `${endpoint} returned a schema contract this client cannot read: ${err instanceof Error ? err.message : String(err)}`,
      'invalid-contract',
      endpoint,
    );
  }
}

function contractFor(endpoint: string, fetchContract: DiscoveryFetch): Promise<Contract> {
  let cached = contracts.get(endpoint);
  if (!cached) {
    const created = discover(endpoint, fetchContract);
    cached = created;
    contracts.set(endpoint, created);
    // A failed or refused discovery is not cached: the next call asks again.
    created.catch(() => {
      if (contracts.get(endpoint) === created) contracts.delete(endpoint);
    });
  }
  return cached;
}

/**
 * Check one operation against the endpoint's served contract. Resolves when it can be served,
 * rejects with a SchemaSkewError (kind `unsupported`) naming what cannot, or with the discovery
 * failure. Never modifies the document or the variables.
 */
export async function checkOperation(
  endpoint: string,
  fetchContract: DiscoveryFetch,
  query: string,
  variables: unknown,
): Promise<void> {
  const { schema } = await contractFor(endpoint, fetchContract);
  const graphql = (await import('graphql')) as typeof import('graphql') & {
    validateInputValue?: (...args: unknown[]) => void;
  };
  const { parse, validate, typeFromAST, Kind } = graphql;

  let document;
  try {
    document = parse(query);
  } catch {
    // A document this client cannot even parse is the caller's defect, not skew; let the
    // server give its own answer rather than inventing one.
    return;
  }
  const problems: string[] = validate(schema, document).map((e) => e.message);

  const operations = document.definitions.filter((d) => d.kind === Kind.OPERATION_DEFINITION);
  if (problems.length === 0 && operations.length === 1) {
    // Variables travel as JSON: coerce exactly that shape, nested input objects included, so a
    // field this instance's input type lacks is refused here and never silently dropped.
    const sent = JSON.parse(JSON.stringify(variables ?? {})) as Record<string, unknown>;
    for (const def of operations[0].variableDefinitions ?? []) {
      const name = def.variable.name.value;
      const type = typeFromAST(schema, def.type);
      if (!type || sent[name] === undefined) continue;
      const report = (path: readonly (string | number)[], error: Error) => {
        problems.push(`variable $${name}${path.length ? `.${path.join('.')}` : ''}: ${error.message}`);
      };
      // The peer range spans two majors: 17 validates with validateInputValue(value, type,
      // onError(error, path)); 16 reports through coerceInputValue(value, type, onError(path,
      // invalid, error)). Both refuse a field the input type does not define.
      if (typeof graphql.validateInputValue === 'function') {
        graphql.validateInputValue(sent[name], type as never, (error: Error, path: readonly (string | number)[]) =>
          report(path, error),
        );
      } else {
        (graphql.coerceInputValue as unknown as (v: unknown, t: unknown, cb: (p: readonly (string | number)[], i: unknown, e: Error) => void) => unknown)(
          sent[name],
          type,
          (path, _invalid, error) => report(path, error),
        );
      }
    }
  }

  if (problems.length > 0) {
    throw new SchemaSkewError(
      `this instance is too old for this request: ${problems.join('; ')}; upgrade the instance to the release this console was built for (${DISCOVERY_RELEASE} or later)`,
      'unsupported',
      endpoint,
    );
  }
}
