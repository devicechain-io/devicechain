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
  DeviceBulkCreateRequest,
  EntityGroupsQuery,
  EntityGroupCreateRequest,
  DeviceProfilesQuery,
  DeviceProfileByTokenQuery,
  DeviceProfileVersionsQuery,
  DeviceProfileCreateRequest,
  MetricDefinitionsQuery,
  MetricDefinitionCreateRequest,
  CommandDefinitionsQuery,
  DeviceCommandVocabularyQuery,
  CommandDefinitionCreateRequest,
  DetectionRulesQuery,
  DetectionRuleCreateRequest,
  ScopeGroupsQuery,
  EntityGroupVersionsQuery,
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
// The uniform entity group (ADR-061). All four registry families share it,
// distinguished by memberType. DeviceGroup et al. are memberType-specialized
// aliases defined alongside their family wrappers below.
export type EntityGroup = EntityGroupsQuery['entityGroups']['results'][number];
export type EntityGroupSearchResults = EntityGroupsQuery['entityGroups'];

export type DeviceProfile = DeviceProfilesQuery['deviceProfiles']['results'][number];
export type DeviceProfileSearchResults = DeviceProfilesQuery['deviceProfiles'];
export type DeviceProfileDetail = NonNullable<DeviceProfileByTokenQuery['deviceProfilesByToken'][number]>;
export type DeviceProfileVersion = DeviceProfileVersionsQuery['deviceProfileVersions'][number];
export type MetricDefinition = MetricDefinitionsQuery['metricDefinitions']['results'][number];
export type CommandDefinition = CommandDefinitionsQuery['commandDefinitions']['results'][number];
// The published vocabulary a device accepts (ADR-043 decision 3) — distinct from
// CommandDefinition, which is the authored draft. A PublishedCommand carries no id or
// token: it is a snapshot copy, and the draft it came from may since have changed.
// Null when the token resolves to no device — a saved view can outlive its device.
export type DeviceCommandVocabulary = DeviceCommandVocabularyQuery['deviceCommandVocabulary'];
export type PublishedCommand = NonNullable<DeviceCommandVocabulary>['commands'][number];
export type DetectionRule = DetectionRulesQuery['detectionRules']['results'][number];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type {
  DeviceTypeCreateRequest,
  DeviceCreateRequest,
  DeviceBulkCreateRequest,
  EntityGroupCreateRequest,
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

const CREATE_DEVICES = graphql(`
  mutation CreateDevices($request: DeviceBulkCreateRequest!) {
    createDevices(request: $request) {
      id
      token
      name
    }
  }
`);

// createDevices renders and creates a whole fleet from a template in one
// transaction (server-side expansion). Returns the created devices; the whole
// batch fails if any rendered token collides or the request is malformed.
export async function createDevices(request: DeviceBulkCreateRequest): Promise<{ token: string }[]> {
  const data = await gql('device-management', CREATE_DEVICES, { request });
  return data.createDevices;
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

// ── Entity groups (ADR-061) ─────────────────────────────────────────────────
//
// The four registry families (device / asset / area / customer) share one uniform
// EntityGroup on the backend, distinguished by memberType. These are the canonical
// group operations; each family's API module re-exports thin wrappers that bake in
// their memberType (see the Device-group wrappers below and areas/assets/customers).

// A group form supplies presentation fields only; the family wrapper injects
// memberType. membershipMode/selector are omitted — dynamic groups are not yet
// authorable (they land with the selector engine, ADR-061 G3/G4).
export type GroupFormRequest = Omit<
  EntityGroupCreateRequest,
  'memberType' | 'membershipMode' | 'selector'
>;

const ENTITY_GROUPS = graphql(`
  query EntityGroups($criteria: EntityGroupSearchCriteria!) {
    entityGroups(criteria: $criteria) {
      results {
        id
        token
        name
        description
        createdAt
        memberType
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listEntityGroups(opts: {
  memberType: string;
  pageNumber: number;
  pageSize: number;
}): Promise<EntityGroupSearchResults> {
  const data = await gql('device-management', ENTITY_GROUPS, {
    criteria: {
      memberType: opts.memberType,
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.entityGroups;
}

// The getter and mutations select the same shape as the EntityGroups query so
// their results stay assignable to the shared EntityGroup type.
const ENTITY_GROUP_BY_TOKEN = graphql(`
  query EntityGroupByToken($tokens: [String!]!) {
    entityGroupsByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
      memberType
    }
  }
`);

export async function getEntityGroup(token: string): Promise<EntityGroup | null> {
  const data = await gql('device-management', ENTITY_GROUP_BY_TOKEN, { tokens: [token] });
  return data.entityGroupsByToken[0] ?? null;
}

// getEntityGroupOfType loads a group and confirms it belongs to the expected
// member family, so a cross-family token (e.g. an asset group's token opened under
// /device-groups) resolves to null rather than mis-rendering in the wrong screen.
// The four family getters below are specializations of it.
export async function getEntityGroupOfType(
  token: string,
  memberType: string,
): Promise<EntityGroup | null> {
  const group = await getEntityGroup(token);
  return group && group.memberType === memberType ? group : null;
}

const CREATE_ENTITY_GROUP = graphql(`
  mutation CreateEntityGroup($request: EntityGroupCreateRequest) {
    createEntityGroup(request: $request) {
      id
      token
      name
      description
      createdAt
      memberType
    }
  }
`);

export async function createEntityGroup(request: EntityGroupCreateRequest): Promise<EntityGroup> {
  const data = await gql('device-management', CREATE_ENTITY_GROUP, { request });
  return data.createEntityGroup;
}

const UPDATE_ENTITY_GROUP = graphql(`
  mutation UpdateEntityGroup($token: String!, $request: EntityGroupCreateRequest) {
    updateEntityGroup(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
      memberType
    }
  }
`);

export async function updateEntityGroup(
  token: string,
  request: EntityGroupCreateRequest,
): Promise<EntityGroup> {
  const data = await gql('device-management', UPDATE_ENTITY_GROUP, { token, request });
  return data.updateEntityGroup;
}

const DELETE_ENTITY_GROUP = graphql(`
  mutation DeleteEntityGroup($token: String!) {
    deleteEntityGroup(token: $token)
  }
`);

export async function deleteEntityGroup(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_ENTITY_GROUP, { token });
  return data.deleteEntityGroup;
}

// ── Device groups (memberType = 'device') ───────────────────────────────────
export type DeviceGroup = EntityGroup;

export const listDeviceGroups = (opts: { pageNumber: number; pageSize: number }) =>
  listEntityGroups({ ...opts, memberType: 'device' });
export const getDeviceGroup = (token: string) => getEntityGroupOfType(token, 'device');
export const createDeviceGroup = (request: GroupFormRequest) =>
  createEntityGroup({ ...request, memberType: 'device' });
export const updateDeviceGroup = (token: string, request: GroupFormRequest) =>
  updateEntityGroup(token, { ...request, memberType: 'device' });
export const deleteDeviceGroup = deleteEntityGroup;

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

const DEVICE_COMMAND_VOCABULARY = graphql(`
  query DeviceCommandVocabulary($deviceToken: String!) {
    deviceCommandVocabulary(deviceToken: $deviceToken) {
      constrained
      commands {
        commandKey
        name
        description
        parameterSchema
      }
    }
  }
`);

// getDeviceCommandVocabulary returns the commands a device ACCEPTS RIGHT NOW — its
// profile's active PUBLISHED version (ADR-043 decision 3), which is what the enqueue
// gate validates against. Prefer this over listCommandDefinitionsForDevice anywhere the
// user is choosing a command to SEND: the draft list can offer commands the server will
// then reject.
//
// `constrained: false` means the profile restricts nothing and the gate accepts any
// command key, with `commands` empty. Do NOT read an empty list as "this device takes no
// commands" — check `constrained`.
export async function getDeviceCommandVocabulary(
  deviceToken: string,
): Promise<DeviceCommandVocabulary> {
  const data = await gql('device-management', DEVICE_COMMAND_VOCABULARY, { deviceToken });
  return data.deviceCommandVocabulary;
}

// listCommandDefinitionsForDevice resolves a device to the command definitions AUTHORED
// on its profile, following the device → type → profile chain (ADR-045): the definitions
// live on the device profile, so a device with no type, or a type with no profile, has
// none. Returns [] when the chain doesn't resolve.
//
// These are DRAFTS. A definition here is not necessarily one the device accepts — it
// accepts what was published. Use getDeviceCommandVocabulary to offer a command for
// sending; use this only to reason about what an author has written down.
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
        authoringGraph
        enabled
        metadata
        entityGroupToken
        entityGroupVersion
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// ── ADR-062 S4 rule scope picker ──────────────────────────────────────────
// A saved dynamic entity group, across ALL member families (device / area / customer /
// asset), that a detection rule can be scoped to. Unlike browse's per-family listing, the
// rule-scope picker lets an author pin a rule to any anchor-able group (e.g. an AREA group
// for the geographic "run this rule for devices in an arid area" case).
export type ScopeGroup = ScopeGroupsQuery['entityGroups']['results'][number];
export type ScopeGroupVersion = EntityGroupVersionsQuery['entityGroupVersions'][number];

const SCOPE_GROUPS = graphql(`
  query ScopeGroups {
    entityGroups(criteria: { pageNumber: 1, pageSize: 500, membershipMode: "dynamic" }) {
      results {
        token
        name
        memberType
        activeVersion
      }
    }
  }
`);

// listScopeGroups returns every dynamic entity group (all families) a rule may scope to.
export async function listScopeGroups(): Promise<ScopeGroup[]> {
  const data = await gql('device-management', SCOPE_GROUPS, undefined);
  return data.entityGroups.results;
}

const ENTITY_GROUP_VERSIONS = graphql(`
  query EntityGroupVersions($token: String!) {
    entityGroupVersions(token: $token) {
      version
      selector
      memberType
      label
    }
  }
`);

// listEntityGroupVersions returns a group's published versions (newest first), each with its
// FROZEN selector — the exact set the pinned version matches, used to preview the scoped count.
export async function listEntityGroupVersions(token: string): Promise<ScopeGroupVersion[]> {
  const data = await gql('device-management', ENTITY_GROUP_VERSIONS, { token });
  return data.entityGroupVersions;
}

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
