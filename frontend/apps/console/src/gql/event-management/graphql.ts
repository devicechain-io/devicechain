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