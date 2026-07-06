// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents the dashboard runtime issues to resolve
// datasources and seed history. Like the rest of the package there is no codegen —
// the SDK runs in documentMode 'string', so a raw query string cast to
// TypedDocument<Result, Vars> is exactly what a generated document is at runtime.
// Each doc targets one service area (see the `gql(area, ...)` call sites in
// resolver.ts / history.ts).

import type { TypedDocument } from '@devicechain/client';

// ── device-management: the devices anchored to a target (area/customer/asset) ─
// Filters relationships whose source is a device and whose target is the anchor
// entity; `source { token }` yields each member device's token (measurementStream
// is keyed by token, per ADR-044).

export interface EntityRelationshipsResult {
  entityRelationships: {
    results: Array<{ source: { token: string } }>;
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
          token
        }
      }
    }
  }
` as unknown as TypedDocument<EntityRelationshipsResult, EntityRelationshipsVariables>;

// ── device-management: which of the given device tokens still exist ──────────
// A dashboard references a device by a STABLE token (ADR-044); the token re-key
// dropped the token→id hop that used to fail on a deleted device, so a widget bound to
// a since-deleted device streamed nothing and rendered blank. This existence check
// restores the "unavailable" signal — devicesByToken returns only the tokens that
// resolve, so a missing token is absent from the result.

export interface DevicesByTokenResult {
  devicesByToken: Array<{ token: string }>;
}
export interface DevicesByTokenVariables {
  tokens: string[];
}

export const DEVICES_BY_TOKEN = `
  query DashboardDevicesByToken($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      token
    }
  }
` as unknown as TypedDocument<DevicesByTokenResult, DevicesByTokenVariables>;

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
    deviceToken: string;
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
