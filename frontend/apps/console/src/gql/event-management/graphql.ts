/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** One device constant a draft calculated-metric formula reads as `device.name`. */
export type CalculatedConstant = {
  /** The constant's name, as the formula spells it after `device.`. */
  key: string;
  /** The number the device's metadata holds under that name, as text such as `1.02`. */
  value: string;
};

/** One input a draft calculated-metric formula reads: a measured metric's key and data type. */
export type CalculatedInput = {
  /** The measured metric's data type: DOUBLE, INT or BOOLEAN. */
  dataType: string;
  /** The measured metric's key, as readings and the formula name it. */
  key: string;
};

/** What to fill. */
export type CalculatedMetricBackfillInput = {
  /** Only these devices, at most 1000; omit to fill every device of the profile. */
  deviceTokens?: Array<string> | null | undefined;
  /** Start of the range, an RFC 3339 timestamp. Not older than retention allows. */
  from: string;
  /**
   * The calculated metrics to fill. Omit to fill the metrics that are new in the version, those
   * its previous published version did not calculate; a version with none then needs them named.
   * A metric whose formula changed since an earlier version may be named, and is flagged in
   * warnings.
   */
  metricKeys?: Array<string> | null | undefined;
  /** The token of the device profile. */
  profileToken: string;
  /** End of the range, an RFC 3339 timestamp, not in the future; after from, by at most 90 days. */
  to: string;
  /** The published version to fill; omit for the active version. A draft is not a version. */
  version?: number | null | undefined;
};

/**
 * Where a calculated-metric backfill job stands. QUEUED and RUNNING are active; a tenant may have
 * one active job. SUCCEEDED, CANCELLED and FAILED are final.
 */
export type CalculatedMetricBackfillStatus =
  /** Cancelled. Rows already written stay. */
  | 'CANCELLED'
  /** Stopped on an error, named in failure. Rows already written stay; start a new job to resume. */
  | 'FAILED'
  /** Accepted and waiting for the worker. */
  | 'QUEUED'
  /** The worker is filling it. */
  | 'RUNNING'
  /** Every device and window was filled or skipped, and the rollup was refreshed. */
  | 'SUCCEEDED';

/** A relationship anchor to filter events by: events recorded against this entity. */
export type EventAnchor = {
  /** Token of the entity, unique within the tenant. */
  token: string;
  /** Entity class: one of device, asset, area, customer or group. Any other value is an error. */
  type: string;
};

/**
 * Filters and paging for event searches. All supplied filters must match. Results are newest first
 * by occurred time.
 */
export type EventSearchCriteria = {
  /** Only events recorded against this entity. */
  anchor?: EventAnchor | null | undefined;
  /** Only events from this device. */
  deviceToken?: string | null | undefined;
  /**
   * Only events that occurred at or before this instant (inclusive), as an RFC 3339 timestamp.
   * Omit for no upper bound. A value that is not RFC 3339 is an error.
   */
  endTime?: string | null | undefined;
  /**
   * Only events of these types, as integers (0 new relationship, 1 location, 2 measurement, 3
   * alert, 4 state change, 5 command invocation, 6 command response). An empty list applies no
   * type filter.
   */
  eventTypes?: Array<number> | null | undefined;
  /** Page to return, starting at 1; values below 1 are treated as 1. */
  pageNumber: number;
  /** Results per page. Values below 1 become 100; values above 1000 are capped at 1000. */
  pageSize: number;
  /**
   * Only events that occurred at or after this instant (inclusive), as an RFC 3339 timestamp.
   * Omit for no lower bound. A value that is not RFC 3339 is an error.
   */
  startTime?: string | null | undefined;
};

/**
 * Filters for a server-side, time-bucketed measurement aggregation. The device, event type, time
 * range and anchor filters work as in EventSearchCriteria, with an added filter on measurement
 * name. startTime is required; a range that would produce more than 10,000 buckets, or a
 * result of more than 50,000 rows (one per bucket per measurement name), is refused with
 * LIMIT_EXCEEDED, so narrow the range, filter on a name or widen intervalSeconds.
 */
export type MeasurementAggregationCriteria = {
  /**
   * Only measurements recorded against this entity. Anchored aggregations are computed from the
   * individual readings.
   */
  anchor?: EventAnchor | null | undefined;
  /** Only measurements from this device. */
  deviceToken?: string | null | undefined;
  /**
   * Only readings that occurred at or before this instant, as an RFC 3339 timestamp. Omit to use
   * the current time. Must not be before startTime. When the aggregation is served from one-minute summaries (see intervalSeconds)
   * the bound is applied at whole-minute granularity, so readings later in the same minute are
   * included.
   */
  endTime?: string | null | undefined;
  /**
   * Only readings whose event type is in this list, as integers. An empty list applies no type
   * filter.
   */
  eventTypes?: Array<number> | null | undefined;
  /**
   * Width of each time bucket in seconds; must be 1 or more (smaller is an error). A width that
   * is a multiple of 60 without an anchor filter is normally served from pre-computed one-minute
   * summaries (an operator setting can turn that off); any other width is computed from the
   * individual readings. Bucket boundaries are fixed instants shared by every query of the same width, not aligned to startTime.
   */
  intervalSeconds: number;
  /** Only measurements with this name. Omit to aggregate every measurement name separately. */
  name?: string | null | undefined;
  /**
   * Only readings that occurred at or after this instant, as an RFC 3339 timestamp. Required;
   * omitting it is an error. When the aggregation is served from one-minute summaries (see intervalSeconds)
   * the bound is applied at whole-minute granularity, so readings earlier in the same minute are
   * included.
   */
  startTime?: string | null | undefined;
};

export type EventsQueryVariables = Exact<{
  criteria: EventSearchCriteria;
}>;


export type EventsQuery = { events: { results: Array<{ id: string, deviceToken: string, eventType: number, occurredTime: string | null, source: string }>, pagination: { totalRecords: number | null } } };

export type CalculatedEventsQueryVariables = Exact<{
  deviceToken: string;
  expression: string;
  dataType: string;
  inputs: Array<CalculatedInput> | CalculatedInput;
  minValue?: number | null | undefined;
  maxValue?: number | null | undefined;
  constants?: Array<CalculatedConstant> | CalculatedConstant | null | undefined;
  from: string;
  to: string;
}>;


export type CalculatedEventsQuery = { calculatedEvents: { computed: number, inputDecimalPlaces: number, samples: Array<{ occurredTime: string, eventId: string, value: string | null, skippedReason: string | null, inputs: Array<{ key: string, value: string }> }>, skipped: Array<{ reason: string, count: number }> } };

export type StartCalculatedMetricBackfillMutationVariables = Exact<{
  input: CalculatedMetricBackfillInput;
}>;


export type StartCalculatedMetricBackfillMutation = { startCalculatedMetricBackfill: { token: string, status: CalculatedMetricBackfillStatus } };

export type CalculatedMetricBackfillAvailabilityQueryVariables = Exact<{ [key: string]: never; }>;


export type CalculatedMetricBackfillAvailabilityQuery = { calculatedMetricBackfillAvailability: { available: boolean, reason: string | null } };

export type CancelCalculatedMetricBackfillMutationVariables = Exact<{
  token: string;
}>;


export type CancelCalculatedMetricBackfillMutation = { cancelCalculatedMetricBackfill: { token: string, status: CalculatedMetricBackfillStatus, cancelRequestedAt: string | null } };

export type CalculatedMetricBackfillsQueryVariables = Exact<{
  profileToken?: string | null | undefined;
  first?: number | null | undefined;
}>;


export type CalculatedMetricBackfillsQuery = { calculatedMetricBackfills: { nextCursor: string | null, nodes: Array<{ token: string, status: CalculatedMetricBackfillStatus, profileToken: string, profileVersion: number, metricKeys: Array<string>, warnings: Array<string>, from: string, to: string, devicesTotal: number | null, devicesDone: number, progress: number | null, samples: number, computed: number, written: number, alreadyPresent: number, windowsCompressed: number, windowsBeyondRetention: number, windowsRefreshed: number, storedInputPrecision: string, failure: string | null, createdAt: string, createdBy: string | null, startedAt: string | null, finishedAt: string | null, cancelRequestedAt: string | null, skipped: Array<{ reason: string, count: number }> }> } };

export type HourlyMeasurementCountsQueryVariables = Exact<{
  criteria: MeasurementAggregationCriteria;
}>;


export type HourlyMeasurementCountsQuery = { bucketedMeasurements: Array<{ bucketStart: string, count: number }> };

export class TypedDocumentString<TResult, TVariables>
  extends String
  implements DocumentTypeDecoration<TResult, TVariables>
{
  __apiType?: NonNullable<DocumentTypeDecoration<TResult, TVariables>['__apiType']>;
  private value: string;
  public __meta__?: Record<string, any> | undefined;

  constructor(value: string, __meta__?: Record<string, any> | undefined) {
    super(value);
    this.value = value;
    this.__meta__ = __meta__;
  }

  override toString(): string & DocumentTypeDecoration<TResult, TVariables> {
    return this.value;
  }
}

export const EventsDocument = new TypedDocumentString(`
    query Events($criteria: EventSearchCriteria!) {
  events(criteria: $criteria) {
    results {
      id
      deviceToken
      eventType
      occurredTime
      source
    }
    pagination {
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<EventsQuery, EventsQueryVariables>;
export const CalculatedEventsDocument = new TypedDocumentString(`
    query CalculatedEvents($deviceToken: String!, $expression: String!, $dataType: String!, $inputs: [CalculatedInput!]!, $minValue: Float, $maxValue: Float, $constants: [CalculatedConstant!], $from: String!, $to: String!) {
  calculatedEvents(
    deviceToken: $deviceToken
    expression: $expression
    dataType: $dataType
    inputs: $inputs
    minValue: $minValue
    maxValue: $maxValue
    constants: $constants
    from: $from
    to: $to
  ) {
    samples {
      occurredTime
      eventId
      inputs {
        key
        value
      }
      value
      skippedReason
    }
    computed
    skipped {
      reason
      count
    }
    inputDecimalPlaces
  }
}
    `) as unknown as TypedDocumentString<CalculatedEventsQuery, CalculatedEventsQueryVariables>;
export const StartCalculatedMetricBackfillDocument = new TypedDocumentString(`
    mutation StartCalculatedMetricBackfill($input: CalculatedMetricBackfillInput!) {
  startCalculatedMetricBackfill(input: $input) {
    token
    status
  }
}
    `) as unknown as TypedDocumentString<StartCalculatedMetricBackfillMutation, StartCalculatedMetricBackfillMutationVariables>;
export const CalculatedMetricBackfillAvailabilityDocument = new TypedDocumentString(`
    query CalculatedMetricBackfillAvailability {
  calculatedMetricBackfillAvailability {
    available
    reason
  }
}
    `) as unknown as TypedDocumentString<CalculatedMetricBackfillAvailabilityQuery, CalculatedMetricBackfillAvailabilityQueryVariables>;
export const CancelCalculatedMetricBackfillDocument = new TypedDocumentString(`
    mutation CancelCalculatedMetricBackfill($token: String!) {
  cancelCalculatedMetricBackfill(token: $token) {
    token
    status
    cancelRequestedAt
  }
}
    `) as unknown as TypedDocumentString<CancelCalculatedMetricBackfillMutation, CancelCalculatedMetricBackfillMutationVariables>;
export const CalculatedMetricBackfillsDocument = new TypedDocumentString(`
    query CalculatedMetricBackfills($profileToken: String, $first: Int) {
  calculatedMetricBackfills(profileToken: $profileToken, first: $first) {
    nodes {
      token
      status
      profileToken
      profileVersion
      metricKeys
      warnings
      from
      to
      devicesTotal
      devicesDone
      progress
      samples
      computed
      written
      alreadyPresent
      windowsCompressed
      windowsBeyondRetention
      windowsRefreshed
      skipped {
        reason
        count
      }
      storedInputPrecision
      failure
      createdAt
      createdBy
      startedAt
      finishedAt
      cancelRequestedAt
    }
    nextCursor
  }
}
    `) as unknown as TypedDocumentString<CalculatedMetricBackfillsQuery, CalculatedMetricBackfillsQueryVariables>;
export const HourlyMeasurementCountsDocument = new TypedDocumentString(`
    query HourlyMeasurementCounts($criteria: MeasurementAggregationCriteria!) {
  bucketedMeasurements(criteria: $criteria) {
    bucketStart
    count
  }
}
    `) as unknown as TypedDocumentString<HourlyMeasurementCountsQuery, HourlyMeasurementCountsQueryVariables>;