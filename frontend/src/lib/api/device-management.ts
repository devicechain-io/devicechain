// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@/lib/graphql/client';

export interface DeviceType {
  id: string;
  token: string;
  name: string | null;
  description: string | null;
  imageUrl: string | null;
  icon: string | null;
  backgroundColor: string | null;
  foregroundColor: string | null;
  borderColor: string | null;
  metadata: string | null;
  createdAt: string | null;
  updatedAt: string | null;
}

export interface Device {
  id: string;
  token: string;
  name: string | null;
  description: string | null;
  deviceType: DeviceType;
  metadata: string | null;
  createdAt: string | null;
  updatedAt: string | null;
}

export interface Pagination {
  pageStart: number | null;
  pageEnd: number | null;
  totalRecords: number | null;
}

export interface DeviceSearchResults {
  results: Device[];
  pagination: Pagination;
}

export interface DeviceTypeSearchResults {
  results: DeviceType[];
  pagination: Pagination;
}

// ── Devices ─────────────────────────────────────────────────────────────

const DEVICES = `
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
`;

export async function listDevices(opts: {
  pageNumber: number;
  pageSize: number;
  deviceType?: string;
}): Promise<DeviceSearchResults> {
  const data = await gql<{ devices: DeviceSearchResults }>('device-management', DEVICES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      deviceType: opts.deviceType ?? null,
    },
  });
  return data.devices;
}

// ── Device types ────────────────────────────────────────────────────────

const DEVICE_TYPES = `
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
`;

export async function listDeviceTypes(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<DeviceTypeSearchResults> {
  const data = await gql<{ deviceTypes: DeviceTypeSearchResults }>(
    'device-management',
    DEVICE_TYPES,
    {
      criteria: {
        pageNumber: opts.pageNumber,
        pageSize: opts.pageSize,
      },
    },
  );
  return data.deviceTypes;
}
