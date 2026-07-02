/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type EventAnchor = {
  id: string;
  type: string;
};

export type EventSearchCriteria = {
  anchor?: EventAnchor | null | undefined;
  deviceId?: string | null | undefined;
  endTime?: string | null | undefined;
  eventTypes?: Array<number> | null | undefined;
  pageNumber: number;
  pageSize: number;
  startTime?: string | null | undefined;
};

export type EventsQueryVariables = Exact<{
  criteria: EventSearchCriteria;
}>;


export type EventsQuery = { events: { results: Array<{ id: string, deviceId: string, eventType: number, occurredTime: string | null, source: string }>, pagination: { totalRecords: number | null } } };

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
      deviceId
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