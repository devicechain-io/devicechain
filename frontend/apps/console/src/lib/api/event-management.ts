// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the event-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-management';
import type {
  EventsQuery,
  CalculatedEventsQuery,
  CalculatedEventsQueryVariables,
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
