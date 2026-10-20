// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the event-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-management';
import type {
  EventsQuery,
  CalculatedEventsQuery,
  CalculatedEventsQueryVariables,
  CalculatedMetricBackfillsQuery,
  StartCalculatedMetricBackfillMutationVariables,
} from '@/gql/event-management/graphql';

// Public type derived from the generated operation result so it always reflects
// the actual selection set and can never drift from the schema.
export type DeviceEvent = EventsQuery['events']['results'][number];

const EVENTS = graphql(`
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
`);

export async function listEvents(opts: {
  deviceToken: string;
  pageNumber: number;
  pageSize: number;
}): Promise<EventsQuery['events']> {
  return (
    await gql('event-management', EVENTS, {
      criteria: {
        pageNumber: opts.pageNumber,
        pageSize: opts.pageSize,
        deviceToken: opts.deviceToken,
      },
    })
  ).events;
}

// ── Draft-formula history preview ─────────────────────────────────────────
// A calculated metric's DRAFT formula evaluated over one device's stored readings. Inputs are
// read back from storage (to inputDecimalPlaces places), so a value can differ from the one
// computed from the device's full-precision reading when it is recorded. Nothing is stored.

const CALCULATED_EVENTS = graphql(`
  query CalculatedEvents(
    $deviceToken: String!
    $expression: String!
    $dataType: String!
    $inputs: [CalculatedInput!]!
    $minValue: Float
    $maxValue: Float
    $constants: [CalculatedConstant!]
    $from: String!
    $to: String!
  ) {
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
`);

export type CalculatedEventsResult = CalculatedEventsQuery['calculatedEvents'];

/** Evaluates a draft formula over a device's stored readings in [from, to]. Stores nothing. */
export async function calculatedEvents(vars: CalculatedEventsQueryVariables): Promise<CalculatedEventsResult> {
  const data = await gql('event-management', CALCULATED_EVENTS, vars);
  return data.calculatedEvents;
}

// Calculated-metric backfill: fills calculated metrics for stored readings that have none.

export type CalculatedMetricBackfillJob = CalculatedMetricBackfillsQuery['calculatedMetricBackfills']['nodes'][number];

const START_BACKFILL = graphql(`
  mutation StartCalculatedMetricBackfill($input: CalculatedMetricBackfillInput!) {
    startCalculatedMetricBackfill(input: $input) {
      token
      status
    }
  }
`);

/** Queues a backfill job and returns its token. Throws UNAVAILABLE when the worker is off. */
export async function startCalculatedMetricBackfill(
  input: StartCalculatedMetricBackfillMutationVariables['input'],
): Promise<string> {
  const data = await gql('event-management', START_BACKFILL, { input });
  return data.startCalculatedMetricBackfill.token;
}

const BACKFILL_AVAILABILITY = graphql(`
  query CalculatedMetricBackfillAvailability {
    calculatedMetricBackfillAvailability {
      available
      reason
    }
  }
`);

/**
 * Whether this instance can run a backfill at all. When it cannot, reason is the same text a
 * refused start (UNAVAILABLE) would carry. Asked once, up front, so the control can be disabled
 * before anyone presses it.
 */
export async function getCalculatedMetricBackfillAvailability(): Promise<{ available: boolean; reason: string | null }> {
  const data = await gql('event-management', BACKFILL_AVAILABILITY, {});
  const a = data.calculatedMetricBackfillAvailability;
  return { available: a.available, reason: a.reason ?? null };
}

const CANCEL_BACKFILL = graphql(`
  mutation CancelCalculatedMetricBackfill($token: String!) {
    cancelCalculatedMetricBackfill(token: $token) {
      token
      status
      cancelRequestedAt
    }
  }
`);

export async function cancelCalculatedMetricBackfill(token: string): Promise<void> {
  await gql('event-management', CANCEL_BACKFILL, { token });
}

const BACKFILLS = graphql(`
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
`);

/** The newest backfill jobs of a profile, newest first. */
export async function listCalculatedMetricBackfills(
  profileToken: string,
  first = 20,
): Promise<CalculatedMetricBackfillJob[]> {
  const data = await gql('event-management', BACKFILLS, { profileToken, first });
  return data.calculatedMetricBackfills.nodes;
}
