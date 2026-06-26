// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/device-management';
import type {
  DevicesQuery,
  DeviceTypesQuery,
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Device = DevicesQuery['devices']['results'][number];
export type DeviceType = DeviceTypesQuery['deviceTypes']['results'][number];
export type Pagination = DevicesQuery['devices']['pagination'];
export type DeviceSearchResults = DevicesQuery['devices'];
export type DeviceTypeSearchResults = DeviceTypesQuery['deviceTypes'];

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
