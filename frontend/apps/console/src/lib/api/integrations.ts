// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for inbound integrations, served by device-management: a tenant's
// authenticated sender of device data over HTTP (a LoRaWAN network server, a webhook), the
// devices such a sender named that the tenant has no device for, and the bearer token the
// sender presents.
//
// 🔴 THE BEARER TOKEN IS HELD NOWHERE IN THIS FILE. The server returns it once, in the response
// to createIntegration or rotateIntegrationToken, and keeps only a digest, so there is no query
// that could read it back and none is written here. The two wrappers below hand it to the
// caller as an ordinary return value and keep no copy: no cache, no module state, no logging.
// What the caller does with it (show it once, then drop it) is
// routes/integrations/IntegrationTokenReveal.tsx.
//
// Every operation needs device:read (reads) or device:write (writes) and an UNSCOPED caller:
// the server refuses the lot to a member limited to part of the tenant.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  IntegrationsQuery,
  IntegrationCreateRequest,
  IntegrationUpdateRequest,
} from '@/gql/device-management/graphql';

export type Integration = IntegrationsQuery['integrations']['results'][number];
export type IntegrationSearchResults = IntegrationsQuery['integrations'];
export type { IntegrationCreateRequest, IntegrationUpdateRequest };

/** The four kinds of sender; fixed at creation. */
export const INTEGRATION_TYPES = ['CHIRPSTACK_V4', 'TTS_V3', 'GENERIC_JSON', 'GENERIC_BINARY'] as const;
export type IntegrationType = (typeof INTEGRATION_TYPES)[number];

/** An integration as the server returned it together with the token it just generated. */
export interface IssuedIntegrationToken {
  integration: Integration;
  /** Shown once. Never stored by the caller beyond the dialog that displays it. */
  issuedToken: string;
}

const INTEGRATIONS = graphql(`
  query Integrations($criteria: IntegrationSearchCriteria!) {
    integrations(criteria: $criteria) {
      results {
        id
        token
        name
        description
        type
        enabled
        identity
        generic
        linkMetrics
        tokenIssuedAt
        undecodedCount
        lastUndecodedAt
        lastUndecodedSample
        lastAuthFailureAt
        createdAt
        updatedAt
        deviceTypes {
          id
          token
          name
        }
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listIntegrations(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<IntegrationSearchResults> {
  const data = await gql('device-management', INTEGRATIONS, {
    criteria: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  return data.integrations;
}

const INTEGRATION = graphql(`
  query IntegrationByToken($token: String!) {
    integration(token: $token) {
      id
      token
      name
      description
      type
      enabled
      identity
      generic
      linkMetrics
      tokenIssuedAt
      undecodedCount
      lastUndecodedAt
      lastUndecodedSample
      lastAuthFailureAt
      createdAt
      updatedAt
      deviceTypes {
        id
        token
        name
      }
    }
  }
`);

export async function getIntegration(token: string): Promise<Integration | null> {
  const data = await gql('device-management', INTEGRATION, { token });
  return data.integration ?? null;
}

const CREATE_INTEGRATION = graphql(`
  mutation CreateIntegration($request: IntegrationCreateRequest!) {
    createIntegration(request: $request) {
      issuedToken
      integration {
        id
        token
        name
        description
        type
        enabled
        identity
        generic
        linkMetrics
        tokenIssuedAt
        undecodedCount
        lastUndecodedAt
        lastUndecodedSample
        lastAuthFailureAt
        createdAt
        updatedAt
        deviceTypes {
          id
          token
          name
        }
      }
    }
  }
`);

/** Creates an integration. The returned token is the only time it can be seen. */
export async function createIntegration(request: IntegrationCreateRequest): Promise<IssuedIntegrationToken> {
  const data = await gql('device-management', CREATE_INTEGRATION, { request });
  return data.createIntegration;
}

const ROTATE_TOKEN = graphql(`
  mutation RotateIntegrationToken($token: String!) {
    rotateIntegrationToken(token: $token) {
      issuedToken
      integration {
        id
        token
        name
        description
        type
        enabled
        identity
        generic
        linkMetrics
        tokenIssuedAt
        undecodedCount
        lastUndecodedAt
        lastUndecodedSample
        lastAuthFailureAt
        createdAt
        updatedAt
        deviceTypes {
          id
          token
          name
        }
      }
    }
  }
`);

/** Replaces the bearer token. The old one stops working at once, with no overlap. */
export async function rotateIntegrationToken(token: string): Promise<IssuedIntegrationToken> {
  const data = await gql('device-management', ROTATE_TOKEN, { token });
  return data.rotateIntegrationToken;
}

const UPDATE_INTEGRATION = graphql(`
  mutation UpdateIntegration($token: String!, $request: IntegrationUpdateRequest!) {
    updateIntegration(token: $token, request: $request) {
      id
      token
      name
      description
      type
      enabled
      identity
      generic
      linkMetrics
      tokenIssuedAt
      undecodedCount
      lastUndecodedAt
      lastUndecodedSample
      lastAuthFailureAt
      createdAt
      updatedAt
      deviceTypes {
        id
        token
        name
      }
    }
  }
`);

/**
 * A PARTIAL update: pass only what changed. An omitted field is left alone; null clears the
 * name and the description. Neither the token nor the type can be changed.
 */
export async function updateIntegration(
  token: string,
  request: IntegrationUpdateRequest,
): Promise<Integration> {
  const data = await gql('device-management', UPDATE_INTEGRATION, { token, request });
  return data.updateIntegration;
}

const DELETE_INTEGRATION = graphql(`
  mutation DeleteIntegration($token: String!) {
    deleteIntegration(token: $token)
  }
`);

/** The sender stops being accepted at once. Aliases on devices are not touched. */
export async function deleteIntegration(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_INTEGRATION, { token });
  return data.deleteIntegration;
}

const UNREGISTERED = graphql(`
  query UnregisteredIntegrationDevices($criteria: UnregisteredIntegrationDeviceSearchCriteria!) {
    unregisteredIntegrationDevices(criteria: $criteria) {
      results {
        id
        integration
        scheme
        value
        firstSeen
        lastSeen
        count
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export type UnregisteredDevices = Awaited<ReturnType<typeof listUnregisteredDevices>>;
export type UnregisteredDevice = UnregisteredDevices['results'][number];

/** The identifiers an integration has seen that no device in scope carries, newest first. */
export async function listUnregisteredDevices(opts: { integration: string; pageNumber: number; pageSize: number }) {
  const data = await gql('device-management', UNREGISTERED, { criteria: opts });
  return data.unregisteredIntegrationDevices;
}

const CLEAR_UNREGISTERED = graphql(`
  mutation ClearUnregisteredIntegrationDevices($integration: String!) {
    clearUnregisteredIntegrationDevices(integration: $integration)
  }
`);

/** Empties the list. They are observations: ones the sender still sends are recorded again. */
export async function clearUnregisteredDevices(integration: string): Promise<number> {
  const data = await gql('device-management', CLEAR_UNREGISTERED, { integration });
  return data.clearUnregisteredIntegrationDevices;
}
