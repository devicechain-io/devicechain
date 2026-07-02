// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for device credentials (ADR-014), served by the
// device-management schema. The secret (credentialValue) is write-only on the
// API: it is submitted on create and never returned on read.
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
    }
  }
`);

const DELETE_DEVICE_CREDENTIAL = graphql(`
  mutation DeleteDeviceCredential($token: String!) {
    deleteDeviceCredential(token: $token)
  }
`);

// List a device's credentials (newest-first is not guaranteed; the set is small).
// Requires device:read.
export async function listDeviceCredentials(deviceToken: string): Promise<DeviceCredential[]> {
  const data = await gql('device-management', DEVICE_CREDENTIALS, {
    criteria: { pageNumber: 1, pageSize: 100, device: deviceToken },
  });
  return data.deviceCredentials.results;
}

// Register a credential for a device. The caller supplies a fresh unique token
// (the credential's own id) and the credential id/secret. Requires device:write.
export async function createDeviceCredential(request: DeviceCredentialCreateRequest) {
  return (await gql('device-management', CREATE_DEVICE_CREDENTIAL, { request })).createDeviceCredential;
}

// Delete a credential by its token; returns whether one was removed. device:write.
export async function deleteDeviceCredential(token: string): Promise<boolean> {
  return (await gql('device-management', DELETE_DEVICE_CREDENTIAL, { token })).deleteDeviceCredential;
}
