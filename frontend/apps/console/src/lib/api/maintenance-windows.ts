// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for maintenance windows, served by device-management.
//
// A window is a recurring weekly schedule in an IANA time zone. `slots` is an opaque JSON
// STRING on the wire — the console builds and reads it in routes/maintenance-windows/slots.ts.
// The SERVER is the only calculator of openings: anything this console shows about when a
// window is open comes from `maintenanceWindowOpening`, never from arithmetic done here.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  MaintenanceWindowsQuery,
  MaintenanceWindowOpeningQuery,
  MaintenanceWindowCreateRequest,
  MaintenanceWindowUpdateRequest,
} from '@/gql/device-management/graphql';

export type MaintenanceWindow = MaintenanceWindowsQuery['maintenanceWindows']['results'][number];
export type MaintenanceWindowSearchResults = MaintenanceWindowsQuery['maintenanceWindows'];
export type MaintenanceWindowOpening = MaintenanceWindowOpeningQuery['maintenanceWindowOpening'];
export type { MaintenanceWindowCreateRequest, MaintenanceWindowUpdateRequest };

const MAINTENANCE_WINDOWS = graphql(`
  query MaintenanceWindows($criteria: MaintenanceWindowSearchCriteria!) {
    maintenanceWindows(criteria: $criteria) {
      results {
        id
        token
        name
        description
        timezone
        slots
        metadata
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

export async function listMaintenanceWindows(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<MaintenanceWindowSearchResults> {
  const data = await gql('device-management', MAINTENANCE_WINDOWS, {
    criteria: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  return data.maintenanceWindows;
}

// The getter and the mutations select the same shape as the list so every result stays
// assignable to the shared MaintenanceWindow type.
const MAINTENANCE_WINDOW_BY_TOKEN = graphql(`
  query MaintenanceWindowByToken($tokens: [String!]!) {
    maintenanceWindowsByToken(tokens: $tokens) {
      id
      token
      name
      description
      timezone
      slots
      metadata
      createdAt
      updatedAt
    }
  }
`);

export async function getMaintenanceWindow(token: string): Promise<MaintenanceWindow | null> {
  const data = await gql('device-management', MAINTENANCE_WINDOW_BY_TOKEN, { tokens: [token] });
  return data.maintenanceWindowsByToken[0] ?? null;
}

const CREATE_MAINTENANCE_WINDOW = graphql(`
  mutation CreateMaintenanceWindow($request: MaintenanceWindowCreateRequest!) {
    createMaintenanceWindow(request: $request) {
      id
      token
      name
      description
      timezone
      slots
      metadata
      createdAt
      updatedAt
    }
  }
`);

export async function createMaintenanceWindow(
  request: MaintenanceWindowCreateRequest,
): Promise<MaintenanceWindow> {
  const data = await gql('device-management', CREATE_MAINTENANCE_WINDOW, { request });
  return data.createMaintenanceWindow;
}

const UPDATE_MAINTENANCE_WINDOW = graphql(`
  mutation UpdateMaintenanceWindow($token: String!, $request: MaintenanceWindowUpdateRequest!) {
    updateMaintenanceWindow(token: $token, request: $request) {
      id
      token
      name
      description
      timezone
      slots
      metadata
      createdAt
      updatedAt
    }
  }
`);

/**
 * A PARTIAL update: pass only what changed. An omitted field is left alone; `timezone` and
 * `slots` refuse an explicit null.
 */
export async function updateMaintenanceWindow(
  token: string,
  request: MaintenanceWindowUpdateRequest,
): Promise<MaintenanceWindow> {
  const data = await gql('device-management', UPDATE_MAINTENANCE_WINDOW, { token, request });
  return data.updateMaintenanceWindow;
}

const DELETE_MAINTENANCE_WINDOW = graphql(`
  mutation DeleteMaintenanceWindow($token: String!) {
    deleteMaintenanceWindow(token: $token)
  }
`);

/** Refused by the server while a device type still uses the window. */
export async function deleteMaintenanceWindow(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_MAINTENANCE_WINDOW, { token });
  return data.deleteMaintenanceWindow;
}

const MAINTENANCE_WINDOW_OPENING = graphql(`
  query MaintenanceWindowOpening($token: String!, $at: String) {
    maintenanceWindowOpening(token: $token, at: $at) {
      found
      openNow
      dispatchAfter
      closes
    }
  }
`);

/** The server's answer to "when may an operation under this window start?" (now when `at` is omitted). */
export async function getMaintenanceWindowOpening(
  token: string,
  at?: string,
): Promise<MaintenanceWindowOpening> {
  const data = await gql('device-management', MAINTENANCE_WINDOW_OPENING, { token, at });
  return data.maintenanceWindowOpening;
}
