// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for device self-registration, served by device-management:
// provisioning profiles, and the per-device provenance and earmark that go with them.
//
// 🔴 THE PROVISION SECRET IS HELD NOWHERE IN THIS FILE. The server returns it once, in the
// response to createProvisioningProfile or rotateProvisioningProfileSecret, and keeps only a
// digest, so there is no query that could read it back and none is written here. The two
// wrappers below hand it to the caller as an ordinary return value and keep no copy: no cache,
// no module state, no logging. What the caller does with it (show it once, then drop it) is
// routes/provisioning/SecretReveal.tsx.
//
// Two reads are for UNSCOPED callers only. `Device.expectedProvisioningProfile` and
// `DeviceCredential.provisioningProfile` answer an error, not null, to a member limited to
// part of the tenant (a customer user), so selecting them on the shared device queries would
// fail the whole page for such a member. They live in their own documents, which the pages
// issue only when the caller is not scoped.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  ProvisioningProfilesQuery,
  ProvisioningProfileCreateRequest,
  ProvisioningProfileUpdateRequest,
} from '@/gql/device-management/graphql';

export type ProvisioningProfile = ProvisioningProfilesQuery['provisioningProfiles']['results'][number];
export type ProvisioningProfileSearchResults = ProvisioningProfilesQuery['provisioningProfiles'];
export type { ProvisioningProfileCreateRequest, ProvisioningProfileUpdateRequest };

/** The two strategies a profile can carry; the server refuses anything else. */
export const PROVISIONING_STRATEGIES = ['ALLOW_NEW', 'CHECK_PRE_PROVISIONED'] as const;
export type ProvisioningStrategy = (typeof PROVISIONING_STRATEGIES)[number];

/** A profile as the server returned it together with the secret it just generated. */
export interface IssuedProfileSecret {
  profile: ProvisioningProfile;
  /** Shown once. Never stored by the caller beyond the dialog that displays it. */
  issuedSecret: string;
}

const PROFILES = graphql(`
  query ProvisioningProfiles($criteria: ProvisioningProfileSearchCriteria!) {
    provisioningProfiles(criteria: $criteria) {
      results {
        id
        token
        name
        description
        provisionKey
        strategy
        credentialType
        enabled
        expiresAt
        maxDevices
        provisionedDeviceCount
        secretIssued
        createdAt
        updatedAt
        deviceType {
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

export async function listProvisioningProfiles(opts: {
  pageNumber: number;
  pageSize: number;
  deviceType?: string;
}): Promise<ProvisioningProfileSearchResults> {
  const data = await gql('device-management', PROFILES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      deviceType: opts.deviceType ?? null,
    },
  });
  return data.provisioningProfiles;
}

const PROFILE_BY_TOKEN = graphql(`
  query ProvisioningProfileByToken($tokens: [String!]!) {
    provisioningProfilesByToken(tokens: $tokens) {
      id
      token
      name
      description
      provisionKey
      strategy
      credentialType
      enabled
      expiresAt
      maxDevices
      provisionedDeviceCount
      secretIssued
      createdAt
      updatedAt
      deviceType {
        id
        token
        name
      }
    }
  }
`);

export async function getProvisioningProfile(token: string): Promise<ProvisioningProfile | null> {
  const data = await gql('device-management', PROFILE_BY_TOKEN, { tokens: [token] });
  return data.provisioningProfilesByToken[0] ?? null;
}

const CREATE_PROFILE = graphql(`
  mutation CreateProvisioningProfile($request: ProvisioningProfileCreateRequest) {
    createProvisioningProfile(request: $request) {
      issuedSecret
      profile {
        id
        token
        name
        description
        provisionKey
        strategy
        credentialType
        enabled
        expiresAt
        maxDevices
        provisionedDeviceCount
        secretIssued
        createdAt
        updatedAt
        deviceType {
          id
          token
          name
        }
      }
    }
  }
`);

/**
 * The server returns no secret for a certificate profile (one that authenticates devices by
 * client certificate). This screen only creates and rotates secret profiles, so a missing
 * secret here is a fault to surface, not a state to render.
 */
function withSecret<T extends { issuedSecret: string | null }>(result: T): T & { issuedSecret: string } {
  if (result.issuedSecret === null) {
    throw new Error('the server issued no provision secret for this profile');
  }
  return { ...result, issuedSecret: result.issuedSecret };
}

/** Creates a profile. The returned secret is the only time it can be seen. */
export async function createProvisioningProfile(
  request: ProvisioningProfileCreateRequest,
): Promise<IssuedProfileSecret> {
  const data = await gql('device-management', CREATE_PROFILE, { request });
  return withSecret(data.createProvisioningProfile);
}

const ROTATE_SECRET = graphql(`
  mutation RotateProvisioningProfileSecret($token: String!) {
    rotateProvisioningProfileSecret(token: $token) {
      issuedSecret
      profile {
        id
        token
        name
        description
        provisionKey
        strategy
        credentialType
        enabled
        expiresAt
        maxDevices
        provisionedDeviceCount
        secretIssued
        createdAt
        updatedAt
        deviceType {
          id
          token
          name
        }
      }
    }
  }
`);

/**
 * Replaces the profile's secret. The old one stops working at once, with no overlap; devices
 * already registered are unaffected. The new secret is returned once.
 */
export async function rotateProvisioningProfileSecret(token: string): Promise<IssuedProfileSecret> {
  const data = await gql('device-management', ROTATE_SECRET, { token });
  return withSecret(data.rotateProvisioningProfileSecret);
}

const UPDATE_PROFILE = graphql(`
  mutation UpdateProvisioningProfile($token: String!, $request: ProvisioningProfileUpdateRequest!) {
    updateProvisioningProfile(token: $token, request: $request) {
      id
      token
      name
      description
      provisionKey
      strategy
      credentialType
      enabled
      expiresAt
      maxDevices
      provisionedDeviceCount
      secretIssued
      createdAt
      updatedAt
      deviceType {
        id
        token
        name
      }
    }
  }
`);

/**
 * A PARTIAL update: pass only what changed. An omitted field is left alone; null clears the
 * fields that can be cleared (description, expiry, cap). The secret is not editable here.
 */
export async function updateProvisioningProfile(
  token: string,
  request: ProvisioningProfileUpdateRequest,
): Promise<ProvisioningProfile> {
  const data = await gql('device-management', UPDATE_PROFILE, { token, request });
  return data.updateProvisioningProfile;
}

const DELETE_PROFILE = graphql(`
  mutation DeleteProvisioningProfile($token: String!) {
    deleteProvisioningProfile(token: $token)
  }
`);

/** Refused while any credential the profile issued still exists (revoked ones count). */
export async function deleteProvisioningProfile(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_PROFILE, { token });
  return data.deleteProvisioningProfile;
}

const REVOKE_DEVICES = graphql(`
  mutation RevokeProvisionedDevices($profileToken: String!) {
    revokeProvisionedDevices(profileToken: $profileToken)
  }
`);

/** Disables every credential the profile issued and ends those sessions. Returns how many. */
export async function revokeProvisionedDevices(profileToken: string): Promise<number> {
  const data = await gql('device-management', REVOKE_DEVICES, { profileToken });
  return data.revokeProvisionedDevices;
}

const RESET_DEVICE = graphql(`
  mutation ResetDeviceProvisioning($deviceToken: String!) {
    resetDeviceProvisioning(deviceToken: $deviceToken)
  }
`);

/** Removes the credentials self-registration issued to the device so it can register again. */
export async function resetDeviceProvisioning(deviceToken: string): Promise<number> {
  const data = await gql('device-management', RESET_DEVICE, { deviceToken });
  return data.resetDeviceProvisioning;
}

const EARMARKED_COUNT = graphql(`
  query EarmarkedDeviceCount($profile: String!) {
    devices(criteria: { pageNumber: 1, pageSize: 1, expectedProvisioningProfile: $profile }) {
      pagination {
        totalRecords
      }
    }
  }
`);

/** How many devices are earmarked for the profile (an unknown token matches none). */
export async function countEarmarkedDevices(profileToken: string): Promise<number> {
  const data = await gql('device-management', EARMARKED_COUNT, { profile: profileToken });
  return data.devices.pagination.totalRecords ?? 0;
}

// ── Per-device provenance (unscoped callers only) ────────────────────────

const DEVICE_PROVISIONING = graphql(`
  query DeviceProvisioning($tokens: [String!]!, $device: String!) {
    devicesByToken(tokens: $tokens) {
      token
      expectedProvisioningProfile {
        token
        name
      }
    }
    deviceCredentials(criteria: { pageNumber: 1, pageSize: 100, device: $device }) {
      results {
        token
        enabled
        provisioningProfile {
          token
          name
        }
      }
    }
  }
`);

export interface ProfileRef {
  token: string;
  name: string | null;
}

export interface DeviceProvisioning {
  /** The profile the device is earmarked for, or null. */
  earmark: ProfileRef | null;
  /** The profiles whose registration issued a credential the device holds. */
  issued: { profile: ProfileRef; credentialToken: string; enabled: boolean }[];
}

/**
 * Where a device stands with self-registration: its earmark and the credentials a profile
 * issued it. Needs device:write (the credential list does) and an UNSCOPED caller.
 */
export async function getDeviceProvisioning(deviceToken: string): Promise<DeviceProvisioning> {
  const data = await gql('device-management', DEVICE_PROVISIONING, {
    tokens: [deviceToken],
    device: deviceToken,
  });
  return {
    earmark: data.devicesByToken[0]?.expectedProvisioningProfile ?? null,
    issued: data.deviceCredentials.results.flatMap((c) =>
      c.provisioningProfile
        ? [{ profile: c.provisioningProfile, credentialToken: c.token, enabled: c.enabled }]
        : [],
    ),
  };
}

const DEVICE_EARMARKS = graphql(`
  query DeviceEarmarks($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      token
      expectedProvisioningProfile {
        token
        name
      }
    }
  }
`);

/** device token -> earmark, for the rows of a device list. Unscoped callers only. */
export async function getDeviceEarmarks(deviceTokens: string[]): Promise<Record<string, ProfileRef>> {
  if (deviceTokens.length === 0) return {};
  const data = await gql('device-management', DEVICE_EARMARKS, { tokens: deviceTokens });
  const out: Record<string, ProfileRef> = {};
  for (const d of data.devicesByToken) {
    if (d.expectedProvisioningProfile) out[d.token] = d.expectedProvisioningProfile;
  }
  return out;
}
