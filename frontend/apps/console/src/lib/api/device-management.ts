// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  DevicesQuery,
  DeviceTypesQuery,
  DeviceTypeCreateRequest,
  DeviceCreateRequest,
  DeviceGroupsQuery,
  DeviceGroupCreateRequest,
  DeviceProfilesQuery,
  DeviceProfileByTokenQuery,
  DeviceProfileVersionsQuery,
  DeviceProfileCreateRequest,
  MetricDefinitionsQuery,
  MetricDefinitionCreateRequest,
  CommandDefinitionsQuery,
  CommandDefinitionCreateRequest,
  DetectionRulesQuery,
  DetectionRuleCreateRequest,
  DeviceFacet,
} from '@/gql/device-management/graphql';

export type { DeviceFacet };

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Device = DevicesQuery['devices']['results'][number];
export type DeviceType = DeviceTypesQuery['deviceTypes']['results'][number];
export type Pagination = DevicesQuery['devices']['pagination'];
export type DeviceSearchResults = DevicesQuery['devices'];
export type DeviceTypeSearchResults = DeviceTypesQuery['deviceTypes'];
export type DeviceGroup = DeviceGroupsQuery['deviceGroups']['results'][number];
export type DeviceGroupSearchResults = DeviceGroupsQuery['deviceGroups'];

export type DeviceProfile = DeviceProfilesQuery['deviceProfiles']['results'][number];
export type DeviceProfileSearchResults = DeviceProfilesQuery['deviceProfiles'];
export type DeviceProfileDetail = NonNullable<DeviceProfileByTokenQuery['deviceProfilesByToken'][number]>;
export type DeviceProfileVersion = DeviceProfileVersionsQuery['deviceProfileVersions'][number];
export type MetricDefinition = MetricDefinitionsQuery['metricDefinitions']['results'][number];
export type CommandDefinition = CommandDefinitionsQuery['commandDefinitions']['results'][number];
export type DetectionRule = DetectionRulesQuery['detectionRules']['results'][number];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type {
  DeviceTypeCreateRequest,
  DeviceCreateRequest,
  DeviceGroupCreateRequest,
  DeviceProfileCreateRequest,
  MetricDefinitionCreateRequest,
  CommandDefinitionCreateRequest,
  DetectionRuleCreateRequest,
};

// ── Devices ─────────────────────────────────────────────────────────────

const DEVICES = graphql(`
  query Devices($criteria: DeviceSearchCriteria!) {
    devices(criteria: $criteria) {
      results {
        id
        token
        name
        description
        createdAt
        deviceType {
          id
          token
          name
          icon
          backgroundColor
          foregroundColor
          borderColor
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

export async function listDevices(opts: {
  pageNumber: number;
  pageSize: number;
  deviceType?: string;
}): Promise<DeviceSearchResults> {
  const data = await gql('device-management', DEVICES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      deviceType: opts.deviceType ?? null,
    },
  });
  return data.devices;
}

const DEVICE_BY_TOKEN = graphql(`
  query DeviceByToken($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
      deviceType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function getDevice(token: string): Promise<Device | null> {
  const data = await gql('device-management', DEVICE_BY_TOKEN, { tokens: [token] });
  return data.devicesByToken[0] ?? null;
}

const CREATE_DEVICE = graphql(`
  mutation CreateDevice($request: DeviceCreateRequest) {
    createDevice(request: $request) {
      id
      token
      name
      description
      createdAt
      deviceType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function createDevice(request: DeviceCreateRequest): Promise<Device> {
  const data = await gql('device-management', CREATE_DEVICE, { request });
  return data.createDevice;
}

const UPDATE_DEVICE = graphql(`
  mutation UpdateDevice($token: String!, $request: DeviceCreateRequest) {
    updateDevice(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
      deviceType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function updateDevice(token: string, request: DeviceCreateRequest): Promise<Device> {
  const data = await gql('device-management', UPDATE_DEVICE, { token, request });
  return data.updateDevice;
}

const DELETE_DEVICE = graphql(`
  mutation DeleteDevice($token: String!) {
    deleteDevice(token: $token)
  }
`);

export async function deleteDevice(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_DEVICE, { token });
  return data.deleteDevice;
}

// ── Device types ────────────────────────────────────────────────────────

const DEVICE_TYPES = graphql(`
  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {
    deviceTypes(criteria: $criteria) {
      results {
        id
        token
        name
        description
        icon
        backgroundColor
        foregroundColor
        borderColor
        imageUrl
        manufacturer
        model
        metadata
        profile {
          token
          name
          category
        }
        createdAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listDeviceTypes(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<DeviceTypeSearchResults> {
  const data = await gql('device-management', DEVICE_TYPES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.deviceTypes;
}

// The device-type getter and mutations select the same shape as the DeviceTypes
// query so their results stay assignable to the shared DeviceType type.
const DEVICE_TYPE_BY_TOKEN = graphql(`
  query DeviceTypeByToken($tokens: [String!]!) {
    deviceTypesByToken(tokens: $tokens) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      manufacturer
      model
      metadata
      profile {
        token
        name
        category
      }
      createdAt
    }
  }
`);

export async function getDeviceType(token: string): Promise<DeviceType | null> {
  const data = await gql('device-management', DEVICE_TYPE_BY_TOKEN, { tokens: [token] });
  return data.deviceTypesByToken[0] ?? null;
}

const CREATE_DEVICE_TYPE = graphql(`
  mutation CreateDeviceType($request: DeviceTypeCreateRequest) {
    createDeviceType(request: $request) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      manufacturer
      model
      metadata
      profile {
        token
        name
        category
      }
      createdAt
    }
  }
`);

export async function createDeviceType(request: DeviceTypeCreateRequest): Promise<DeviceType> {
  const data = await gql('device-management', CREATE_DEVICE_TYPE, { request });
  return data.createDeviceType;
}

const UPDATE_DEVICE_TYPE = graphql(`
  mutation UpdateDeviceType($token: String!, $request: DeviceTypeCreateRequest) {
    updateDeviceType(token: $token, request: $request) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      manufacturer
      model
      metadata
      profile {
        token
        name
        category
      }
      createdAt
    }
  }
`);

export async function updateDeviceType(
  token: string,
  request: DeviceTypeCreateRequest,
): Promise<DeviceType> {
  const data = await gql('device-management', UPDATE_DEVICE_TYPE, { token, request });
  return data.updateDeviceType;
}

// updateDeviceType is a full replace, so any field omitted from the request is
// nulled. A partial editor (the basic form, the appearance form, the profile
// control) therefore has to carry forward every field it does not itself edit;
// this returns that carry-forward base so the three editors share one field list
// instead of each maintaining a copy that can drift (the ADR-045 preservation
// trap). Callers spread it and override only what they change.
//
// The `satisfies` guard makes a future field added to DeviceTypeCreateRequest a
// compile error here until it is carried forward too — the whole point of a single
// source of truth. Every request field must therefore be listed (the DeviceType
// selection sets carry them all so this can), so `profileToken` maps from the
// nested `profile` object.
export function deviceTypePreserved(dt: DeviceType): DeviceTypeCreateRequest {
  // Values use `?? null` (not undefined): the generated input fields are
  // `string | null`, and for a full replace an explicit null and an omitted field
  // both land as a nil *string server-side.
  return {
    token: dt.token,
    name: dt.name ?? null,
    description: dt.description ?? null,
    imageUrl: dt.imageUrl ?? null,
    icon: dt.icon ?? null,
    backgroundColor: dt.backgroundColor ?? null,
    foregroundColor: dt.foregroundColor ?? null,
    borderColor: dt.borderColor ?? null,
    profileToken: dt.profile?.token ?? null,
    manufacturer: dt.manufacturer ?? null,
    model: dt.model ?? null,
    metadata: dt.metadata ?? null,
  } satisfies Required<DeviceTypeCreateRequest>;
}

const DELETE_DEVICE_TYPE = graphql(`
  mutation DeleteDeviceType($token: String!) {
    deleteDeviceType(token: $token)
  }
`);

export async function deleteDeviceType(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_DEVICE_TYPE, { token });
  return data.deleteDeviceType;
}

// ── Device groups ─────────────────────────────────────────────────────────

const DEVICE_GROUPS = graphql(`
  query DeviceGroups($criteria: DeviceGroupSearchCriteria!) {
    deviceGroups(criteria: $criteria) {
      results {
        id
        token
        name
        description
        createdAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listDeviceGroups(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<DeviceGroupSearchResults> {
  const data = await gql('device-management', DEVICE_GROUPS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.deviceGroups;
}

// The device-group getter and mutations select the same shape as the DeviceGroups
// query so their results stay assignable to the shared DeviceGroup type.
const DEVICE_GROUP_BY_TOKEN = graphql(`
  query DeviceGroupByToken($tokens: [String!]!) {
    deviceGroupsByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function getDeviceGroup(token: string): Promise<DeviceGroup | null> {
  const data = await gql('device-management', DEVICE_GROUP_BY_TOKEN, { tokens: [token] });
  return data.deviceGroupsByToken[0] ?? null;
}

const CREATE_DEVICE_GROUP = graphql(`
  mutation CreateDeviceGroup($request: DeviceGroupCreateRequest) {
    createDeviceGroup(request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function createDeviceGroup(request: DeviceGroupCreateRequest): Promise<DeviceGroup> {
  const data = await gql('device-management', CREATE_DEVICE_GROUP, { request });
  return data.createDeviceGroup;
}

const UPDATE_DEVICE_GROUP = graphql(`
  mutation UpdateDeviceGroup($token: String!, $request: DeviceGroupCreateRequest) {
    updateDeviceGroup(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function updateDeviceGroup(
  token: string,
  request: DeviceGroupCreateRequest,
): Promise<DeviceGroup> {
  const data = await gql('device-management', UPDATE_DEVICE_GROUP, { token, request });
  return data.updateDeviceGroup;
}

const DELETE_DEVICE_GROUP = graphql(`
  mutation DeleteDeviceGroup($token: String!) {
    deleteDeviceGroup(token: $token)
  }
`);

export async function deleteDeviceGroup(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_DEVICE_GROUP, { token });
  return data.deleteDeviceGroup;
}

// ── Device profiles (ADR-045) ─────────────────────────────────────────────

const DEVICE_PROFILES = graphql(`
  query DeviceProfiles($criteria: DeviceProfileSearchCriteria!) {
    deviceProfiles(criteria: $criteria) {
      results {
        id
        token
        name
        description
        category
        activeVersion
        deviceTypeCount
        metadata
        createdAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listDeviceProfiles(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<DeviceProfileSearchResults> {
  const data = await gql('device-management', DEVICE_PROFILES, {
    criteria: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  return data.deviceProfiles;
}

// The getter + mutations select the same shape as the list so results stay
// assignable to the shared DeviceProfile type.
const DEVICE_PROFILE_BY_TOKEN = graphql(`
  query DeviceProfileByToken($tokens: [String!]!) {
    deviceProfilesByToken(tokens: $tokens) {
      id
      token
      name
      description
      category
      activeVersion
      deviceTypeCount
      metadata
      createdAt
    }
  }
`);

export async function getDeviceProfile(token: string): Promise<DeviceProfileDetail | null> {
  const data = await gql('device-management', DEVICE_PROFILE_BY_TOKEN, { tokens: [token] });
  return data.deviceProfilesByToken[0] ?? null;
}

const CREATE_DEVICE_PROFILE = graphql(`
  mutation CreateDeviceProfile($request: DeviceProfileCreateRequest) {
    createDeviceProfile(request: $request) {
      id
      token
      name
      description
      category
      activeVersion
      deviceTypeCount
      metadata
      createdAt
    }
  }
`);

export async function createDeviceProfile(
  request: DeviceProfileCreateRequest,
): Promise<DeviceProfile> {
  const data = await gql('device-management', CREATE_DEVICE_PROFILE, { request });
  return data.createDeviceProfile;
}

const UPDATE_DEVICE_PROFILE = graphql(`
  mutation UpdateDeviceProfile($token: String!, $request: DeviceProfileCreateRequest) {
    updateDeviceProfile(token: $token, request: $request) {
      id
      token
      name
      description
      category
      activeVersion
      deviceTypeCount
      metadata
      createdAt
    }
  }
`);

export async function updateDeviceProfile(
  token: string,
  request: DeviceProfileCreateRequest,
): Promise<DeviceProfile> {
  const data = await gql('device-management', UPDATE_DEVICE_PROFILE, { token, request });
  return data.updateDeviceProfile;
}

const DELETE_DEVICE_PROFILE = graphql(`
  mutation DeleteDeviceProfile($token: String!) {
    deleteDeviceProfile(token: $token)
  }
`);

export async function deleteDeviceProfile(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_DEVICE_PROFILE, { token });
  return data.deleteDeviceProfile;
}

// ── Device-profile versions (ADR-045 slice c) ─────────────────────────────

const DEVICE_PROFILE_VERSIONS = graphql(`
  query DeviceProfileVersions($token: String!) {
    deviceProfileVersions(token: $token) {
      version
      label
      description
      publishedAt
      publishedBy
    }
  }
`);

export async function listDeviceProfileVersions(token: string): Promise<DeviceProfileVersion[]> {
  const data = await gql('device-management', DEVICE_PROFILE_VERSIONS, { token });
  return data.deviceProfileVersions;
}

// ── Discovery facets (ADR-045 decision 8) ─────────────────────────────────

const FACET_VALUES = graphql(`
  query FacetValues($facet: DeviceFacet!) {
    facetValues(facet: $facet)
  }
`);

// The distinct in-use values of a facet (manufacturer/model/category), suggested
// under the free-text facet inputs. Fetched fresh per input mount so a value just
// added elsewhere shows up next time; the list is small and off any hot path.
export async function getFacetValues(facet: DeviceFacet): Promise<string[]> {
  const data = await gql('device-management', FACET_VALUES, { facet });
  return data.facetValues;
}

const PUBLISH_DEVICE_PROFILE = graphql(`
  mutation PublishDeviceProfile($token: String!, $label: String, $description: String) {
    publishDeviceProfile(token: $token, label: $label, description: $description) {
      version
    }
  }
`);

export async function publishDeviceProfile(
  token: string,
  label?: string,
  description?: string,
): Promise<number> {
  const data = await gql('device-management', PUBLISH_DEVICE_PROFILE, {
    token,
    label: label ?? null,
    description: description ?? null,
  });
  return data.publishDeviceProfile.version;
}

const ROLLBACK_DEVICE_PROFILE = graphql(`
  mutation RollbackDeviceProfile($token: String!, $version: Int!) {
    rollbackDeviceProfile(token: $token, version: $version) {
      token
      activeVersion
    }
  }
`);

export async function rollbackDeviceProfile(token: string, version: number): Promise<void> {
  await gql('device-management', ROLLBACK_DEVICE_PROFILE, { token, version });
}

// ── Metric definitions (ADR-016) ──────────────────────────────────────────

const METRIC_DEFINITIONS = graphql(`
  query MetricDefinitions($criteria: MetricDefinitionSearchCriteria!) {
    metricDefinitions(criteria: $criteria) {
      results {
        id
        token
        name
        description
        metricKey
        dataType
        unit
        minValue
        maxValue
        enum
        descriptor
        metadata
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// A profile's metric vocabulary is small and rendered whole in a tab, so load one
// large page rather than paginating inside the panel.
export async function listMetricDefinitions(profileToken: string): Promise<MetricDefinition[]> {
  const data = await gql('device-management', METRIC_DEFINITIONS, {
    criteria: { pageNumber: 1, pageSize: 1000, deviceProfile: profileToken },
  });
  return data.metricDefinitions.results;
}

const CREATE_METRIC_DEFINITION = graphql(`
  mutation CreateMetricDefinition($request: MetricDefinitionCreateRequest) {
    createMetricDefinition(request: $request) {
      id
      token
    }
  }
`);

export async function createMetricDefinition(request: MetricDefinitionCreateRequest): Promise<void> {
  await gql('device-management', CREATE_METRIC_DEFINITION, { request });
}

const UPDATE_METRIC_DEFINITION = graphql(`
  mutation UpdateMetricDefinition($token: String!, $request: MetricDefinitionCreateRequest) {
    updateMetricDefinition(token: $token, request: $request) {
      id
      token
    }
  }
`);

export async function updateMetricDefinition(
  token: string,
  request: MetricDefinitionCreateRequest,
): Promise<void> {
  await gql('device-management', UPDATE_METRIC_DEFINITION, { token, request });
}

const DELETE_METRIC_DEFINITION = graphql(`
  mutation DeleteMetricDefinition($token: String!) {
    deleteMetricDefinition(token: $token)
  }
`);

export async function deleteMetricDefinition(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_METRIC_DEFINITION, { token });
  return data.deleteMetricDefinition;
}

// ── Command definitions (ADR-043) ─────────────────────────────────────────

const COMMAND_DEFINITIONS = graphql(`
  query CommandDefinitions($criteria: CommandDefinitionSearchCriteria!) {
    commandDefinitions(criteria: $criteria) {
      results {
        id
        token
        name
        description
        commandKey
        parameterSchema
        metadata
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listCommandDefinitions(profileToken: string): Promise<CommandDefinition[]> {
  const data = await gql('device-management', COMMAND_DEFINITIONS, {
    criteria: { pageNumber: 1, pageSize: 1000, deviceProfile: profileToken },
  });
  return data.commandDefinitions.results;
}

// listCommandDefinitionsForDevice resolves a device to the command definitions it
// accepts, following the device → type → profile chain (ADR-045): the definitions live
// on the device profile, so a device with no type, or a type with no profile, has none.
// Used by the dashboard command-button authoring UI, which scopes to a device but needs
// that device's command vocabulary. Returns [] when the chain doesn't resolve.
export async function listCommandDefinitionsForDevice(
  deviceToken: string,
): Promise<CommandDefinition[]> {
  const device = await getDevice(deviceToken);
  const typeToken = device?.deviceType?.token;
  if (!typeToken) return [];
  const deviceType = await getDeviceType(typeToken);
  const profileToken = deviceType?.profile?.token;
  if (!profileToken) return [];
  return listCommandDefinitions(profileToken);
}

const CREATE_COMMAND_DEFINITION = graphql(`
  mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {
    createCommandDefinition(request: $request) {
      id
      token
    }
  }
`);

export async function createCommandDefinition(
  request: CommandDefinitionCreateRequest,
): Promise<void> {
  await gql('device-management', CREATE_COMMAND_DEFINITION, { request });
}

const UPDATE_COMMAND_DEFINITION = graphql(`
  mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {
    updateCommandDefinition(token: $token, request: $request) {
      id
      token
    }
  }
`);

export async function updateCommandDefinition(
  token: string,
  request: CommandDefinitionCreateRequest,
): Promise<void> {
  await gql('device-management', UPDATE_COMMAND_DEFINITION, { token, request });
}

const DELETE_COMMAND_DEFINITION = graphql(`
  mutation DeleteCommandDefinition($token: String!) {
    deleteCommandDefinition(token: $token)
  }
`);

export async function deleteCommandDefinition(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_COMMAND_DEFINITION, { token });
  return data.deleteCommandDefinition;
}

// ── Detection rules (ADR-051 / ADR-057) ───────────────────────────────────
//
// A DETECT rule authored on a profile — the single alarm-authoring path since the
// 6d cutover (the retired AlarmDefinition's replacement). `definition` is the opaque
// rules.Rule JSON document; device-management stores it whole and checks only JSON
// well-formedness on write, while event-processing performs the authoritative
// type/cost/injection validation when the profile is published. Like the metric and
// command definitions these are DRAFT edits that take effect only on publish.

const DETECTION_RULES = graphql(`
  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {
    detectionRules(criteria: $criteria) {
      results {
        id
        token
        name
        description
        definition
        enabled
        metadata
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// A profile's rule set is small and rendered whole in a tab, so load one large page.
export async function listDetectionRules(profileToken: string): Promise<DetectionRule[]> {
  const data = await gql('device-management', DETECTION_RULES, {
    criteria: { pageNumber: 1, pageSize: 1000, deviceProfile: profileToken },
  });
  return data.detectionRules.results;
}

const CREATE_DETECTION_RULE = graphql(`
  mutation CreateDetectionRule($request: DetectionRuleCreateRequest!) {
    createDetectionRule(request: $request) {
      id
      token
    }
  }
`);

export async function createDetectionRule(request: DetectionRuleCreateRequest): Promise<void> {
  await gql('device-management', CREATE_DETECTION_RULE, { request });
}

const UPDATE_DETECTION_RULE = graphql(`
  mutation UpdateDetectionRule($token: String!, $request: DetectionRuleCreateRequest!) {
    updateDetectionRule(token: $token, request: $request) {
      id
      token
    }
  }
`);

export async function updateDetectionRule(
  token: string,
  request: DetectionRuleCreateRequest,
): Promise<void> {
  await gql('device-management', UPDATE_DETECTION_RULE, { token, request });
}

const DELETE_DETECTION_RULE = graphql(`
  mutation DeleteDetectionRule($token: String!) {
    deleteDetectionRule(token: $token)
  }
`);

export async function deleteDetectionRule(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_DETECTION_RULE, { token });
  return data.deleteDetectionRule;
}
