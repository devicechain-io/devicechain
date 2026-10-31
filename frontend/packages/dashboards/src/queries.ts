// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents the dashboard runtime issues to resolve
// datasources and seed history. Like the rest of the package there is no codegen —
// the SDK runs in documentMode 'string', so a raw query string cast to
// TypedDocument<Result, Vars> is exactly what a generated document is at runtime.
// Each doc targets one service area (see the `gql(area, ...)` call sites in
// resolver.ts / history.ts).

import type { TypedDocument } from '@devicechain/client';

// ── device-management: the devices of a customer anchor ─────────────────────
// A customer anchor reads customerDevices rather than the relationship graph: the graph is
// closed to a member limited to some customers (a customer user), and customerDevices
// answers such a member about its own customers. For every other viewer it answers what
// the relationship search did for (device -> customer, type), so ONE read serves
// every viewer. It carries each device's name, so the roster needs no second read.

export interface CustomerDevicesResult {
  customerDevices: {
    results: Array<{ token: string; name: string | null; deviceType?: { token: string } | null }>;
    pagination?: { totalRecords: number | null } | null;
  };
}
export interface CustomerDevicesVariables {
  criteria: {
    pageNumber: number;
    pageSize: number;
    customer: string;
    relationshipType: string;
  };
}

export const CUSTOMER_DEVICES = `
  query DashboardCustomerDevices($criteria: CustomerDeviceSearchCriteria!) {
    customerDevices(criteria: $criteria) {
      results {
        token
        name
        deviceType {
          token
        }
      }
      pagination {
        totalRecords
      }
    }
  }
` as unknown as TypedDocument<CustomerDevicesResult, CustomerDevicesVariables>;

// ── device-management: the devices on an area or an asset anchor ────────────
// anchorDevices answers every viewer for an area or an asset: an unscoped caller gets the devices
// with a live edge to the anchor, and a member limited to some customers gets the same list
// narrowed to the devices it can see (the anchor itself needs no permission of its own, since an
// anchor none of its devices is on answers an empty page). The relationship graph that read this
// before is closed to such a member. relationshipType is optional on the wire and REFUSED for a
// scoped caller (SCOPE_UNSUPPORTED): the resolver sends it first and retries without it.

export interface AnchorDevicesResult {
  anchorDevices: {
    results: Array<{ token: string; name: string | null; deviceType?: { token: string } | null }>;
    pagination?: { totalRecords: number | null } | null;
  };
}
export interface AnchorDevicesVariables {
  criteria: {
    pageNumber: number;
    pageSize: number;
    targetType: string;
    target: string;
    relationshipType?: string | null;
  };
}

export const ANCHOR_DEVICES =
  'query DashboardAnchorDevices($criteria: AnchorDeviceSearchCriteria!){anchorDevices(criteria:$criteria){results{token name deviceType{token}}pagination{totalRecords}}}' as unknown as TypedDocument<AnchorDevicesResult, AnchorDevicesVariables>;

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

// -- device-management: the names of a set of devices (one batched read) ------
// A fleet widget labels its rows with device names, not tokens. devicesByToken takes up to 1000
// tokens, and an anchor resolves to at most one page of 500, so the whole roster's names are ONE
// round trip however large the fleet.

export interface DeviceNamesResult {
  devicesByToken: Array<{ token: string; name: string | null; deviceType?: { token: string } | null }>;
}

export const DEVICE_NAMES = `
  query DashboardDeviceNames($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      token
      name
      deviceType {
        token
      }
    }
  }
` as unknown as TypedDocument<DeviceNamesResult, DevicesByTokenVariables>;

// -- device-management: what a device type's ACTIVE profile version declares ---
// The names a fleet widget reads to tell "does not report this" (the device's profile never
// declares the measurement) from "not reporting" (it declares it and the device is silent). A
// catalogue read: a member limited to some customers may make it. Calculated metrics are part of
// the same list. activeProfileVersion is null until the profile is first published, and profile is
// null on a type with none: neither declares anything, so neither can say a measurement is absent.

export interface DeclaredMetricsResult {
  deviceTypesByToken: Array<{
    token: string;
    profile: { activeProfileVersion: { metrics: Array<{ metricKey: string }> } | null } | null;
  }>;
}
export interface DeclaredMetricsVariables {
  tokens: string[];
}

export const DECLARED_METRICS = `
  query DashboardDeclaredMetrics($tokens: [String!]!) {
    deviceTypesByToken(tokens: $tokens) {
      token
      profile {
        activeProfileVersion {
          metrics {
            metricKey
          }
        }
      }
    }
  }
` as unknown as TypedDocument<DeclaredMetricsResult, DeclaredMetricsVariables>;

// ── device-management: list entities of one kind (root context-selector candidates) ─
// Each anchor target type (customer/area/asset) plus bare devices exposes a
// `<kind>(criteria){results{token name}}` query. A root context-selector lists these so a
// viewer can re-point the dashboard's top-level context (which building/customer). One doc
// per kind since each is a distinct root field; a generous single page (dashboards pick
// among tens, not thousands — nested tree picking is deferred).

export interface EntityListResult {
  results: Array<{ token: string; name: string | null }>;
}
export interface EntityListVariables {
  criteria: {
    pageNumber: number;
    pageSize: number;
    areaTypeToken?: string | null;
    assetTypeToken?: string | null;
    deviceType?: string | null;
  };
}

export const LIST_DEVICES = `
  query DashboardListDevices($criteria: DeviceSearchCriteria!) {
    devices(criteria: $criteria) {
      results {
        token
        name
      }
    }
  }
` as unknown as TypedDocument<{ devices: EntityListResult }, EntityListVariables>;

export const LIST_CUSTOMERS = `
  query DashboardListCustomers($criteria: CustomerSearchCriteria!) {
    customers(criteria: $criteria) {
      results {
        token
        name
      }
    }
  }
` as unknown as TypedDocument<{ customers: EntityListResult }, EntityListVariables>;

export const LIST_AREAS = `
  query DashboardListAreas($criteria: AreaSearchCriteria!) {
    areas(criteria: $criteria) {
      results {
        token
        name
      }
    }
  }
` as unknown as TypedDocument<{ areas: EntityListResult }, EntityListVariables>;

export const LIST_ASSETS = `
  query DashboardListAssets($criteria: AssetSearchCriteria!) {
    assets(criteria: $criteria) {
      results {
        token
        name
      }
    }
  }
` as unknown as TypedDocument<{ assets: EntityListResult }, EntityListVariables>;

// ── event-management: bucketed history for chart seeding ─────────────────────

export interface MeasurementBucket {
  bucketStart: string;
  name: string;
  avg: number | null;
  // The bucket's other aggregates, exact over the readings it holds (null when it holds none).
  // Optional on the type because the chart seed reads only `avg`; the aggregate widgets read
  // these and refuse a bucket that has readings but lacks them.
  min?: number | null;
  max?: number | null;
  sum?: number | null;
  count?: number;
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
      min
      max
      sum
      count
    }
  }
` as unknown as TypedDocument<BucketedMeasurementsResult, BucketedMeasurementsVariables>;
