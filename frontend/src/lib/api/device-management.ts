// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/device-management';
import type {
  DevicesQuery,
  DeviceTypesQuery,
  DeviceTypeCreateRequest,
  DeviceCreateRequest,
  DeviceGroupsQuery,
  DeviceGroupCreateRequest,
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Device = DevicesQuery['devices']['results'][number];
export type DeviceType = DeviceTypesQuery['deviceTypes']['results'][number];
export type Pagination = DevicesQuery['devices']['pagination'];
export type DeviceSearchResults = DevicesQuery['devices'];
export type DeviceTypeSearchResults = DeviceTypesQuery['deviceTypes'];
export type DeviceGroup = DeviceGroupsQuery['deviceGroups']['results'][number];
export type DeviceGroupSearchResults = DeviceGroupsQuery['deviceGroups'];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { DeviceTypeCreateRequest, DeviceCreateRequest, DeviceGroupCreateRequest };

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
          backgroundColor
          foregroundColor
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
        backgroundColor
        foregroundColor
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
        backgroundColor
        foregroundColor
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
        backgroundColor
        foregroundColor
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
