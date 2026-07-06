// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the event-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-management';
import type { EventsQuery } from '@/gql/event-management/graphql';

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
