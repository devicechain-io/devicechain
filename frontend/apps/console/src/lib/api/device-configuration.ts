// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations behind the device configuration panel: the device's
// desired/reported configuration, its revision history, and the configuration keys
// its profile declares, read at device:read; and the two device:write actions that
// restore an earlier revision's content or copy another device's.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  DeviceConfigurationQuery,
  DeviceConfigurationRevisionsQuery,
  DeviceConfigurationDeclarationQuery,
} from '@/gql/device-management/graphql';

export type DeviceConfiguration = NonNullable<DeviceConfigurationQuery['deviceConfiguration']>;
export type DeviceConfigurationRevision =
  DeviceConfigurationRevisionsQuery['deviceConfigurationRevisions']['results'][number];
export type ConfigurationKey = NonNullable<
  NonNullable<
    NonNullable<
      DeviceConfigurationDeclarationQuery['devicesByToken'][number]['deviceType']
    >['profile']
  >['activeProfileVersion']
>['configurationDeclaration'][number];

const DEVICE_CONFIGURATION = graphql(`
  query DeviceConfiguration($deviceToken: String!) {
    deviceConfiguration(deviceToken: $deviceToken) {
      desired {
        revision
        digest
        document
        createdAt
        actor
      }
      reported {
        revision
        digest
        status
        errors
        reportedAt
        lastSyncAt
      }
      pending
      stale
      undeclared
      invalid
    }
  }
`);

const DEVICE_CONFIGURATION_REVISIONS = graphql(`
  query DeviceConfigurationRevisions($deviceToken: String!, $pagination: PaginationInput!) {
    deviceConfigurationRevisions(deviceToken: $deviceToken, pagination: $pagination) {
      results {
        revision
        digest
        document
        createdAt
        actor
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// The declaration on the device's profile, as of its ACTIVE PUBLISHED version — what the
// device actually sees. A never-published profile has no active version and so declares
// nothing, which is exactly what a device of it sees.
const DEVICE_CONFIGURATION_DECLARATION = graphql(`
  query DeviceConfigurationDeclaration($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      token
      deviceType {
        token
        profile {
          token
          activeProfileVersion {
            version
            configurationDeclaration {
              key
              valueType
              description
            }
          }
        }
      }
    }
  }
`);

export interface DeviceConfigurationView {
  // The device the data was read for. The panel renders it only while this equals
  // the device on screen, so a slow response for the previous device never paints.
  deviceToken: string;
  configuration: DeviceConfiguration | null;
  revisions: DeviceConfigurationRevision[];
  totalRevisions: number;
  // null when the declaration could not be read (e.g. no profile authority); the
  // rest of the panel is unaffected.
  declaration: ConfigurationKey[] | null;
}

export const REVISION_PAGE_SIZE = 20;

// Everything the panel shows, read together. `pending` and `stale` are the server's
// verdicts and are passed through untouched.
export async function getDeviceConfigurationView(
  deviceToken: string,
): Promise<DeviceConfigurationView> {
  const [cfg, revs, decl] = await Promise.all([
    gql('device-management', DEVICE_CONFIGURATION, { deviceToken }),
    gql('device-management', DEVICE_CONFIGURATION_REVISIONS, {
      deviceToken,
      pagination: { pageNumber: 1, pageSize: REVISION_PAGE_SIZE },
    }),
    gql('device-management', DEVICE_CONFIGURATION_DECLARATION, { tokens: [deviceToken] }).then(
      (d) => d.devicesByToken[0]?.deviceType?.profile?.activeProfileVersion?.configurationDeclaration ?? [],
      () => null,
    ),
  ]);
  return {
    deviceToken,
    configuration: cfg.deviceConfiguration ?? null,
    revisions: revs.deviceConfigurationRevisions.results,
    totalRevisions: revs.deviceConfigurationRevisions.pagination.totalRecords ?? 0,
    declaration: decl,
  };
}

// The document a device should now hold (its latest revision), or null when it has none
// or no device has the token. The copy action reads the SOURCE device through this to
// preview what the target will become.
export async function getDesiredConfigurationDocument(deviceToken: string): Promise<string | null> {
  const data = await gql('device-management', DEVICE_CONFIGURATION, { deviceToken });
  return data.deviceConfiguration?.desired?.document ?? null;
}

// ── Restore and copy (device:write) ───────────────────────────────────────

const RESTORE_DEVICE_CONFIGURATION = graphql(`
  mutation RestoreDeviceConfiguration($deviceToken: String!, $revision: Int!) {
    restoreDeviceConfiguration(deviceToken: $deviceToken, revision: $revision) {
      desired {
        revision
      }
    }
  }
`);

// Mints a NEW revision with the content of an earlier one; history is never rewritten.
// Answers the device's desired revision number afterwards (unchanged when the device
// already held that content).
export async function restoreDeviceConfiguration(
  deviceToken: string,
  revision: number,
): Promise<number | null> {
  const data = await gql('device-management', RESTORE_DEVICE_CONFIGURATION, { deviceToken, revision });
  return data.restoreDeviceConfiguration.desired?.revision ?? null;
}

const COPY_DEVICE_CONFIGURATION = graphql(`
  mutation CopyDeviceConfiguration($fromDeviceToken: String!, $toDeviceToken: String!) {
    copyDeviceConfiguration(fromDeviceToken: $fromDeviceToken, toDeviceToken: $toDeviceToken) {
      desired {
        revision
      }
    }
  }
`);

// Makes the target's configuration equal to the source's latest revision.
export async function copyDeviceConfiguration(
  fromDeviceToken: string,
  toDeviceToken: string,
): Promise<number | null> {
  const data = await gql('device-management', COPY_DEVICE_CONFIGURATION, {
    fromDeviceToken,
    toDeviceToken,
  });
  return data.copyDeviceConfiguration.desired?.revision ?? null;
}

// ── Profile configuration declaration (authoring) ─────────────────────────

export interface ConfigurationKeyInput {
  key: string;
  valueType: string;
  description?: string | null;
}

// The profile's DRAFT declaration — the editable working copy. A device sees the one
// frozen into the published version (read above), so edits here take effect only when
// the profile is published.
const PROFILE_CONFIGURATION_DECLARATION = graphql(`
  query ProfileConfigurationDeclaration($tokens: [String!]!) {
    deviceProfilesByToken(tokens: $tokens) {
      token
      configurationDeclaration {
        key
        valueType
        description
      }
    }
  }
`);

export async function getProfileConfigurationDeclaration(
  profileToken: string,
): Promise<ConfigurationKey[]> {
  const data = await gql('device-management', PROFILE_CONFIGURATION_DECLARATION, {
    tokens: [profileToken],
  });
  return data.deviceProfilesByToken[0]?.configurationDeclaration ?? [];
}

// Replaces the whole draft declaration: the server validates the list as a unit and
// refuses it entirely if any key is invalid, so a rejected save changes nothing.
const SET_PROFILE_CONFIGURATION_DECLARATION = graphql(`
  mutation SetDeviceProfileConfigurationDeclaration($token: String!, $keys: [ConfigurationKeyInput!]!) {
    setDeviceProfileConfigurationDeclaration(token: $token, keys: $keys) {
      token
      configurationDeclaration {
        key
        valueType
        description
      }
    }
  }
`);

export async function setProfileConfigurationDeclaration(
  profileToken: string,
  keys: ConfigurationKeyInput[],
): Promise<ConfigurationKey[]> {
  const data = await gql('device-management', SET_PROFILE_CONFIGURATION_DECLARATION, {
    token: profileToken,
    keys,
  });
  return data.setDeviceProfileConfigurationDeclaration.configurationDeclaration;
}
