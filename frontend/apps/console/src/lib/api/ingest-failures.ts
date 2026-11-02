// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for the ingest-failures view, served by the event-management schema.
//
// The events the platform refused or could not store, newest first: the answer to "why is my
// device's data not showing up". A record is a report, not a queue; nothing here retries it.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-management';
import type {
  FailedEventsQuery,
  FailedEventsQueryVariables,
  FailedEventSummaryQuery,
} from '@/gql/event-management/graphql';

export type FailedEvent = FailedEventsQuery['failedEvents']['results'][number];
export type FailedEventReason = FailedEvent['reason'];
export type FailedEventReasonCount = FailedEventSummaryQuery['failedEventSummary'][number];

const FAILED_EVENTS = graphql(`
  query FailedEvents($criteria: FailedEventSearchCriteria!) {
    failedEvents(criteria: $criteria) {
      results {
        recordSeq
        occurredTime
        sourceArea
        reason
        reasonCode
        deviceToken
        eventType
        error
        correlation
        suppressed
        overflow
        payloadStatus
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

/** One page of the tenant's failed events, optionally narrowed by reason, device token and time. */
export async function listFailedEvents(criteria: FailedEventsQueryVariables['criteria']) {
  return (await gql('event-management', FAILED_EVENTS, { criteria })).failedEvents;
}

const FAILED_EVENT_SUMMARY = graphql(`
  query FailedEventSummary($since: String!) {
    failedEventSummary(since: $since) {
      reason
      reasonCode
      records
      suppressed
      devices {
        deviceToken
        records
      }
    }
  }
`);

/** Failures per reason since an instant, with the devices with the most of each. */
export async function getFailedEventSummary(since: string): Promise<FailedEventReasonCount[]> {
  return (await gql('event-management', FAILED_EVENT_SUMMARY, { since })).failedEventSummary;
}

const FAILED_EVENT_COUNT = graphql(`
  query FailedEventCount($deviceToken: String!, $since: String!) {
    failedEventCount(deviceToken: $deviceToken, since: $since)
  }
`);

/** How many events were refused for one claimed device token since an instant. */
export async function getFailedEventCount(deviceToken: string, since: string): Promise<number> {
  return (await gql('event-management', FAILED_EVENT_COUNT, { deviceToken, since })).failedEventCount;
}
