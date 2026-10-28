// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for device credentials (ADR-014), served by the
// device-management schema. The secret (credentialValue) is write-only on the
// API: it is submitted on create or rotation and never returned on read (the
// platform keeps only a keyed digest of it). The list query below deliberately does
// not select it. An access token is the same: it is the credentialId of an ACCESS_TOKEN
// credential, stored only as a digest, and returned ONCE, on the create or rotation that
// set it. A read of an ACCESS_TOKEN returns a fingerprint that cannot be presented.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  DeviceCredentialsQuery,
  DeviceCredentialCreateRequest,
} from '@/gql/device-management/graphql';

export type DeviceCredential = DeviceCredentialsQuery['deviceCredentials']['results'][number];
export type { DeviceCredentialCreateRequest };

// The credential types the platform understands (ADR-014).
export const CREDENTIAL_TYPES = ['ACCESS_TOKEN', 'MQTT_BASIC', 'X509_CERTIFICATE'] as const;
export type CredentialType = (typeof CREDENTIAL_TYPES)[number];

const DEVICE_CREDENTIALS = graphql(`
  query DeviceCredentials($criteria: DeviceCredentialSearchCriteria!) {
    deviceCredentials(criteria: $criteria) {
      results {
        id
        token
        credentialType
        credentialId
        enabled
        expiresAt
        createdAt
        certificate {
          fingerprintSha256
          subjectCn
          serialHex
          notAfter
          trustAnchor {
            token
            name
            enabled
          }
        }
      }
      pagination {
        totalRecords
      }
    }
  }
`);

const CREATE_DEVICE_CREDENTIAL = graphql(`
  mutation CreateDeviceCredential($request: DeviceCredentialCreateRequest) {
    createDeviceCredential(request: $request) {
      id
      token
      credentialType
      credentialId
      enabled
      expiresAt
      certificate {
        fingerprintSha256
        subjectCn
        notAfter
      }
    }
  }
`);

const UPDATE_DEVICE_CREDENTIAL = graphql(`
  mutation UpdateDeviceCredential($token: String!, $request: DeviceCredentialUpdateRequest!) {
    updateDeviceCredential(token: $token, request: $request) {
      id
      token
      credentialType
      credentialId
      enabled
    }
  }
`);

const DELETE_DEVICE_CREDENTIAL = graphql(`
  mutation DeleteDeviceCredential($token: String!) {
    deleteDeviceCredential(token: $token)
  }
`);

// List a device's credentials (newest-first is not guaranteed; the set is small).
// Requires device:write, not device:read. For an ACCESS_TOKEN the credentialId read here
// is a fingerprint, not the token, which is only ever shown once.
export async function listDeviceCredentials(deviceToken: string): Promise<DeviceCredential[]> {
  const data = await gql('device-management', DEVICE_CREDENTIALS, {
    criteria: { pageNumber: 1, pageSize: 100, device: deviceToken },
  });
  return data.deviceCredentials.results;
}

// The most X.509 certificate credentials one device may hold (the server's cap; this mirror
// only saves a round trip and is never the authority).
export const MAX_X509_CREDENTIALS_PER_DEVICE = 3;

// Register a credential for a device. The caller supplies a fresh unique token
// (the credential's own id) and the credential id/secret. Requires device:write.
export async function createDeviceCredential(request: DeviceCredentialCreateRequest) {
  return (await gql('device-management', CREATE_DEVICE_CREDENTIAL, { request })).createDeviceCredential;
}

// Set a new secret on an existing credential (rotation). It sends credentialValue
// and nothing else, so no other field on the credential is touched. The old secret
// stops working at once. Requires device:write.
export async function rotateDeviceCredentialSecret(token: string, credentialValue: string) {
  return (await gql('device-management', UPDATE_DEVICE_CREDENTIAL, { token, request: { credentialValue } }))
    .updateDeviceCredential;
}

// Set a new access token on an ACCESS_TOKEN credential (rotation). The old token stops
// working at once. The returned credentialId is the new token and is the only time it is
// readable. Requires device:write.
export async function rotateDeviceCredentialToken(token: string, credentialId: string) {
  return (await gql('device-management', UPDATE_DEVICE_CREDENTIAL, { token, request: { credentialId } }))
    .updateDeviceCredential;
}

// Delete a credential by its token; returns whether one was removed. device:write.
export async function deleteDeviceCredential(token: string): Promise<boolean> {
  return (await gql('device-management', DELETE_DEVICE_CREDENTIAL, { token })).deleteDeviceCredential;
}
