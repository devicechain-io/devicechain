// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for device trust anchors, served by the device-management schema.
//
// A trust anchor is a CA certificate a tenant trusts to vouch for its devices' client
// certificates. It is PUBLIC material (a certificate, never a key), so unlike a credential
// secret it is returned on every read and is shown in full. Reading needs device:read;
// every write needs device:write and an unscoped caller.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  DeviceTrustAnchorsQuery,
  DeviceTrustAnchorCreateRequest,
} from '@/gql/device-management/graphql';

export type TrustAnchor = DeviceTrustAnchorsQuery['deviceTrustAnchors']['results'][number];
export type TrustAnchorSearchResults = DeviceTrustAnchorsQuery['deviceTrustAnchors'];
export type { DeviceTrustAnchorCreateRequest };

const ANCHORS = graphql(`
  query DeviceTrustAnchors($criteria: DeviceTrustAnchorSearchCriteria!) {
    deviceTrustAnchors(criteria: $criteria) {
      results {
        id
        token
        name
        description
        certificatePem
        fingerprintSha256
        subject
        issuer
        notBefore
        notAfter
        enabled
        credentialCount
        profileCount
        createdAt
        updatedAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listTrustAnchors(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<TrustAnchorSearchResults> {
  const data = await gql('device-management', ANCHORS, {
    criteria: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  return data.deviceTrustAnchors;
}

const ANCHOR_BY_TOKEN = graphql(`
  query DeviceTrustAnchorByToken($tokens: [String!]!) {
    deviceTrustAnchorsByToken(tokens: $tokens) {
      id
      token
      name
      description
      certificatePem
      fingerprintSha256
      subject
      issuer
      notBefore
      notAfter
      enabled
      credentialCount
      profileCount
      createdAt
      updatedAt
    }
  }
`);

export async function getTrustAnchor(token: string): Promise<TrustAnchor | null> {
  const data = await gql('device-management', ANCHOR_BY_TOKEN, { tokens: [token] });
  return data.deviceTrustAnchorsByToken[0] ?? null;
}

const CREATE_ANCHOR = graphql(`
  mutation CreateDeviceTrustAnchor($request: DeviceTrustAnchorCreateRequest!) {
    createDeviceTrustAnchor(request: $request) {
      id
      token
      name
      fingerprintSha256
      subject
      notAfter
      enabled
    }
  }
`);

/** The server validates the PEM (one CA certificate, allowed key, no name constraints). */
export async function createTrustAnchor(request: DeviceTrustAnchorCreateRequest) {
  return (await gql('device-management', CREATE_ANCHOR, { request })).createDeviceTrustAnchor;
}

const SET_ANCHOR_ENABLED = graphql(`
  mutation SetDeviceTrustAnchorEnabled($token: String!, $request: DeviceTrustAnchorUpdateRequest!) {
    updateDeviceTrustAnchor(token: $token, request: $request) {
      id
      token
      enabled
    }
  }
`);

/**
 * Enables or disables an anchor, sending `enabled` and nothing else (a partial update).
 * Disabling withdraws the CA at once and ends the live sessions of the devices relying on it.
 */
export async function setTrustAnchorEnabled(token: string, enabled: boolean) {
  return (await gql('device-management', SET_ANCHOR_ENABLED, { token, request: { enabled } }))
    .updateDeviceTrustAnchor;
}

const DELETE_ANCHOR = graphql(`
  mutation DeleteDeviceTrustAnchor($token: String!) {
    deleteDeviceTrustAnchor(token: $token)
  }
`);

/** Refused (REFERENCE_VIOLATION) while any credential or profile still names the anchor. */
export async function deleteTrustAnchor(token: string): Promise<boolean> {
  return (await gql('device-management', DELETE_ANCHOR, { token })).deleteDeviceTrustAnchor;
}
