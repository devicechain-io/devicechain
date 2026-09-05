// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the outbound-connectors service (ADR-060 C5).
// A connector is a tenant-scoped, versioned {type, config, write-only credential}
// target that a REACT `publish` action delivers through. The credential is
// write-only: it goes in on create/update (`secret`) and never comes back out —
// the read side exposes only `hasSecret`.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/outbound-connectors';
import type {
  ConnectorsQuery,
  ConnectorQuery,
  ConnectorVersionsQuery,
} from '@/gql/outbound-connectors/graphql';

export type ConnectorListItem = ConnectorsQuery['connectors']['results'][number];
export type Connector = NonNullable<ConnectorQuery['connector']>;
export type ConnectorSearchResults = ConnectorsQuery['connectors'];
export type ConnectorVersion = ConnectorVersionsQuery['connectorVersions'][number];

// ── Connectors ──────────────────────────────────────────────────────────

// The list omits the `config` blob and `hasSecret` — the table only needs metadata.
const CONNECTORS = graphql(`
  query Connectors($criteria: ConnectorSearchCriteria!) {
    connectors(criteria: $criteria) {
      results {
        token
        name
        description
        type
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listConnectors(opts: {
  pageNumber: number;
  pageSize: number;
  type?: string;
}): Promise<ConnectorSearchResults> {
  const data = await gql('outbound-connectors', CONNECTORS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      type: opts.type ?? null,
    },
  });
  return data.connectors;
}

const CONNECTOR_BY_TOKEN = graphql(`
  query Connector($token: String!) {
    connector(token: $token) {
      id
      token
      name
      description
      type
      config
      hasSecret
      updatedAt
    }
  }
`);

export async function getConnector(token: string): Promise<Connector | null> {
  const data = await gql('outbound-connectors', CONNECTOR_BY_TOKEN, { token });
  return data.connector ?? null;
}

const CONNECTOR_TYPES = graphql(`
  query ConnectorTypes {
    connectorTypes
  }
`);

export async function listConnectorTypes(): Promise<string[]> {
  const data = await gql('outbound-connectors', CONNECTOR_TYPES);
  return data.connectorTypes;
}

const CREATE_CONNECTOR = graphql(`
  mutation CreateConnector($request: ConnectorCreateRequest!) {
    createConnector(request: $request) {
      token
    }
  }
`);

// A `secret` of undefined means "no credential"; a non-empty string seals one.
export async function createConnector(opts: {
  token: string;
  name?: string;
  description?: string;
  type: string;
  config: string;
  secret?: string;
}): Promise<{ token: string }> {
  const data = await gql('outbound-connectors', CREATE_CONNECTOR, {
    request: {
      token: opts.token,
      name: opts.name ?? null,
      description: opts.description ?? null,
      type: opts.type,
      config: opts.config,
      secret: opts.secret ?? null,
    },
  });
  return data.createConnector;
}

// updateConnector is a PARTIAL update of the draft. Every field is three-state: leave
// it `undefined` to say nothing about it (the stored value is kept), pass `null` to
// clear it, or pass a value to set it.
//
// 🔴 `undefined` AND `null` ARE NOW DIFFERENT REQUESTS, which is the whole point of the
// conversion and the one thing to get right at a call site. Under the previous
// full-replace shape every field was written unconditionally, so `?? null` was the
// idiom everywhere and a field the caller had nothing to say about was cleared. Do NOT
// reintroduce `?? null` here: it turns "unchanged" into "clear this".
//
// `type` and `config` sit on NOT NULL columns and REFUSE a null. Changing `type` alone
// re-validates the STORED config against the new kind, so a re-point that leaves the
// config unusable is refused at the write rather than at send time. `secret` follows
// the store's write-only contract: omit it to PRESERVE the stored credential, send a
// value to ROTATE it, or send null (or "") to DELETE it. expectedUpdatedAt is the
// optimistic-concurrency precondition: pass the updatedAt the editor loaded so a save
// fails (CONFLICT) if another writer changed the connector since.
const UPDATE_CONNECTOR = graphql(`
  mutation UpdateConnector(
    $token: String!
    $request: ConnectorUpdateRequest!
    $expectedUpdatedAt: String
  ) {
    updateConnector(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {
      token
      updatedAt
    }
  }
`);

export async function updateConnector(
  token: string,
  input: {
    // undefined ⇒ leave alone, null ⇒ clear, value ⇒ set.
    name?: string | null;
    description?: string | null;
    // Required columns: undefined ⇒ leave alone, value ⇒ set. A null is refused.
    type?: string;
    config?: string;
    // undefined ⇒ preserve, null ⇒ delete, value ⇒ rotate.
    secret?: string | null;
    expectedUpdatedAt?: string | null;
  },
): Promise<{ token: string; updatedAt: string | null }> {
  const { expectedUpdatedAt, ...request } = input;
  const data = await gql('outbound-connectors', UPDATE_CONNECTOR, {
    token,
    // The rest object is forwarded as-is so a key the caller never set stays ABSENT on
    // the wire. Rebuilding it field by field is what would reintroduce `?? null`.
    request,
    expectedUpdatedAt: expectedUpdatedAt ?? null,
  });
  return data.updateConnector;
}

// renameConnector changes a connector's token and nothing else. It is the capability
// updateConnector's payload token used to carry: a blank newToken is refused, renaming
// to the current token is an idempotent success, and a token another connector holds is
// refused by name. The credential is keyed by the connector's immutable id and stays
// bound — but a rule's connector reference names the TOKEN, so rules pointing at a
// renamed connector must be re-pointed too.
const RENAME_CONNECTOR = graphql(`
  mutation RenameConnector($token: String!, $newToken: String!) {
    renameConnector(token: $token, newToken: $newToken) {
      token
      updatedAt
    }
  }
`);

export async function renameConnector(
  token: string,
  newToken: string,
): Promise<{ token: string; updatedAt: string | null }> {
  const data = await gql('outbound-connectors', RENAME_CONNECTOR, { token, newToken });
  return data.renameConnector;
}

// CONFLICT_MARKER matches the backend's optimistic-concurrency error text (the
// outbound-connectors model mirrors dashboard-management's ErrConflict). A save
// that fails the precondition surfaces as a GraphQL error carrying this text.
export const CONFLICT_MARKER = 'modified by another writer';

// ── Versioning (ADR-039) ──────────────────────────────────────────────────

const CONNECTOR_VERSIONS = graphql(`
  query ConnectorVersions($token: String!) {
    connectorVersions(token: $token) {
      version
      type
      label
      description
      publishedAt
      publishedBy
    }
  }
`);

export async function listConnectorVersions(token: string): Promise<ConnectorVersion[]> {
  const data = await gql('outbound-connectors', CONNECTOR_VERSIONS, { token });
  return data.connectorVersions;
}

const PUBLISH_CONNECTOR = graphql(`
  mutation PublishConnector(
    $token: String!
    $label: String
    $description: String
    $expectedUpdatedAt: String
  ) {
    publishConnector(
      token: $token
      label: $label
      description: $description
      expectedUpdatedAt: $expectedUpdatedAt
    ) {
      version
    }
  }
`);

// publishConnector freezes the current (saved) draft into a new immutable version.
// expectedUpdatedAt is the same precondition as updateConnector: publish fails with
// CONFLICT if the server draft moved on since.
export async function publishConnector(
  token: string,
  input: { label?: string; description?: string; expectedUpdatedAt?: string | null },
): Promise<{ version: number }> {
  const data = await gql('outbound-connectors', PUBLISH_CONNECTOR, {
    token,
    label: input.label?.trim() ? input.label.trim() : null,
    description: input.description?.trim() ? input.description.trim() : null,
    expectedUpdatedAt: input.expectedUpdatedAt ?? null,
  });
  return data.publishConnector;
}

const ROLLBACK_CONNECTOR = graphql(`
  mutation RollbackConnector($token: String!, $version: Int!) {
    rollbackConnector(token: $token, version: $version) {
      type
      config
      updatedAt
    }
  }
`);

// rollbackConnector re-drafts a published version's {type, config} into the draft,
// returning the new draft so the editor can re-baseline without a reload. The
// credential is NOT versioned (it is keyed by the connector, resolved live), so a
// rollback leaves the stored secret untouched.
export async function rollbackConnector(
  token: string,
  version: number,
): Promise<{ type: string; config: string; updatedAt: string | null }> {
  const data = await gql('outbound-connectors', ROLLBACK_CONNECTOR, { token, version });
  return data.rollbackConnector;
}

const DELETE_CONNECTOR = graphql(`
  mutation DeleteConnector($token: String!) {
    deleteConnector(token: $token)
  }
`);

export async function deleteConnector(token: string): Promise<boolean> {
  const data = await gql('outbound-connectors', DELETE_CONNECTOR, { token });
  return data.deleteConnector;
}
