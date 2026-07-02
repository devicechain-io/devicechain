// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents the dashboard runtime issues to resolve
// datasources and seed history. Like the rest of the package there is no codegen —
// the SDK runs in documentMode 'string', so a raw query string cast to
// TypedDocument<Result, Vars> is exactly what a generated document is at runtime.
// Each doc targets one service area (see the `gql(area, ...)` call sites in
// resolver.ts / history.ts).

import type { TypedDocument } from '@devicechain/client';

// ── device-management: resolve a device token to its numeric id ──────────────
// measurementStream filters on the numeric Device.id, not the token.

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
