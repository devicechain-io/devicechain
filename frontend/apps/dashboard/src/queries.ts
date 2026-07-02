// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents for the dashboard app.
//
// Like the SDK packages, this app carries no graphql-codegen — the SDK runs in
// documentMode 'string', so a raw query string cast to TypedDocument<Result, Vars>
// is exactly what a generated document is at runtime. Each doc targets one service
// area (see the `gql(area, ...)` call sites).

import type { TypedDocument } from '@devicechain/client';

// ── dashboard-management: load a dashboard's definition ──────────────────────

export interface DashboardResult {
  dashboard: { token: string; name: string | null; definition: string } | null;
}
export interface DashboardVariables {
  token: string;
}

export const DASHBOARD_BY_TOKEN = `
  query Dashboard($token: String!) {
    dashboard(token: $token) {
      token
      name
      definition
    }
  }
` as unknown as TypedDocument<DashboardResult, DashboardVariables>;

// ── dashboard-management: save an edited definition ──────────────────────────

export interface UpdateDashboardResult {
  updateDashboard: { token: string };
}
export interface UpdateDashboardVariables {
  token: string;
  request: { token: string; name?: string | null; definition: string };
}

export const UPDATE_DASHBOARD = `
  mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!) {
    updateDashboard(token: $token, request: $request) {
      token
    }
  }
` as unknown as TypedDocument<UpdateDashboardResult, UpdateDashboardVariables>;

// ── device-management: resolve a device token to its numeric id ──────────────

export interface DevicesByTokenResult {
  devicesByToken: Array<{ id: string; token: string }>;
}
export interface DevicesByTokenVariables {
  tokens: string[];
}

export const DEVICES_BY_TOKEN = `
  query DevicesByToken($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      id
      token
    }
  }
` as unknown as TypedDocument<DevicesByTokenResult, DevicesByTokenVariables>;

// ── device-management: the devices anchored to a target (area/customer/asset) ─
// Filters relationships whose source is a device and whose target is the anchor
// entity; `source { id }` yields each member device's numeric id.

export interface EntityRelationshipsResult {
  entityRelationships: {
    results: Array<{ source: { id: string } }>;
  };
}
export interface EntityRelationshipsVariables {
  criteria: {
    pageNumber: number;
    pageSize: number;
    sourceType: string;
    targetType: string;
    target: string;
    relationshipType?: string | null;
  };
}

export const DEVICES_FOR_ANCHOR = `
  query DevicesForAnchor($criteria: EntityRelationshipSearchCriteria!) {
    entityRelationships(criteria: $criteria) {
      results {
        source {
          id
        }
      }
    }
  }
` as unknown as TypedDocument<EntityRelationshipsResult, EntityRelationshipsVariables>;

// ── event-management: bucketed history for chart seeding ─────────────────────

export interface MeasurementBucket {
  bucketStart: string;
  name: string;
  avg: number | null;
}
export interface BucketedMeasurementsResult {
  bucketedMeasurements: MeasurementBucket[];
}
export interface BucketedMeasurementsVariables {
  criteria: {
    deviceId: string;
    name?: string | null;
    startTime: string;
    endTime: string;
    intervalSeconds: number;
  };
}

export const BUCKETED_MEASUREMENTS = `
  query BucketedMeasurements($criteria: MeasurementAggregationCriteria!) {
    bucketedMeasurements(criteria: $criteria) {
      bucketStart
      name
      avg
    }
  }
` as unknown as TypedDocument<BucketedMeasurementsResult, BucketedMeasurementsVariables>;
