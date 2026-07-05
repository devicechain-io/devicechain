// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for raised alarms (ADR-041) against device-management:
// the paginated alarm query, the operator ack/clear mutations, and the live
// alarm-events subscription document (consumed by useAlarmStream).
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { AlarmsQuery, AlarmStreamSubscription } from '@/gql/device-management/graphql';

// Public types derive from the generated operation results so they always reflect
// the actual selection sets and can never drift from the schema.
export type Alarm = AlarmsQuery['alarms']['results'][number];
export type AlarmSearchResults = AlarmsQuery['alarms'];
// The full subscription payload and the single event within it — the hook sink
// receives the former, the page merge logic consumes the latter.
export type AlarmStreamData = AlarmStreamSubscription;
export type AlarmEvent = AlarmStreamSubscription['alarmStream'];

// ── Query ───────────────────────────────────────────────────────────────

const ALARMS = graphql(`
  query Alarms($criteria: AlarmSearchCriteria!) {
    alarms(criteria: $criteria) {
      results {
        id
        token
        originatorType
        originatorId
        originatorToken
        alarmKey
        metricKey
        state
        acknowledged
        severity
        raisedTime
        clearedTime
        acknowledgedTime
        acknowledgedBy
        lastValue
        message
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listAlarms(opts: {
  pageNumber: number;
  pageSize: number;
  state?: string;
  severity?: string;
  acknowledged?: boolean;
}): Promise<AlarmSearchResults> {
  const data = await gql('device-management', ALARMS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      state: opts.state ?? null,
      severity: opts.severity ?? null,
      acknowledged: opts.acknowledged ?? null,
    },
  });
  return data.alarms;
}

// ── Operator mutations ────────────────────────────────────────────────────
// The acknowledging identity is recorded server-side from the authenticated
// subject; the client supplies only the alarm token. Both return the updated Alarm,
// but the console reconciles by re-running the query, so only the id is selected.

const ACKNOWLEDGE_ALARM = graphql(`
  mutation AcknowledgeAlarm($token: String!) {
    acknowledgeAlarm(token: $token) {
      id
    }
  }
`);

export async function acknowledgeAlarm(token: string): Promise<void> {
  await gql('device-management', ACKNOWLEDGE_ALARM, { token });
}

const CLEAR_ALARM = graphql(`
  mutation ClearAlarm($token: String!) {
    clearAlarm(token: $token) {
      id
    }
  }
`);

export async function clearAlarm(token: string): Promise<void> {
  await gql('device-management', CLEAR_ALARM, { token });
}

// ── Live subscription ───────────────────────────────────────────────────
// The alarm-events stream (ADR-037). Delivery is best-effort (at-most-once): the
// stored Alarm row is the source of truth, so a consumer merging this stream must
// reconcile against the query. The console treats each event as a reconcile trigger,
// not the row source of truth (see useAlarmStream / AlarmsPage). Exported (unlike the
// query/mutation documents) because the hook passes it to the SDK subscribe().
export const ALARM_STREAM = graphql(`
  subscription AlarmStream(
    $originatorType: String
    $originator: String
    $state: String
    $severity: String
    $alarmKey: String
  ) {
    alarmStream(
      originatorType: $originatorType
      originator: $originator
      state: $state
      severity: $severity
      alarmKey: $alarmKey
    ) {
      eventType
      alarmToken
      originatorType
      originatorId
      originatorToken
      alarmKey
      metricKey
      state
      severity
      previousSeverity
      acknowledged
      acknowledgedBy
      lastValue
      message
      raisedTime
      occurredTime
    }
  }
`);
