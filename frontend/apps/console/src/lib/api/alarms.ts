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
  /** Only alarms raised on this one entity: both fields together, e.g. ('device', 'pump-1'). */
  originatorType?: string;
  originatorToken?: string;
}): Promise<AlarmSearchResults> {
  const data = await gql('device-management', ALARMS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      state: opts.state ?? null,
      severity: opts.severity ?? null,
      acknowledged: opts.acknowledged ?? null,
      ...(opts.originatorToken ? { originatorType: opts.originatorType, originatorToken: opts.originatorToken } : {}),
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

const ACKNOWLEDGE_ALARMS = graphql(`
  mutation AcknowledgeAlarms($tokens: [String!]!) {
    acknowledgeAlarms(tokens: $tokens) {
      acknowledged {
        token
      }
      refusals {
        token
        code
        reason
      }
    }
  }
`);

/** The most tokens one acknowledgeAlarms request may name; more is refused outright. */
export const ACKNOWLEDGE_BATCH_LIMIT = 2000;

export interface BulkAcknowledgeOutcome {
  /** Alarms that are now acknowledged, including any that already were. */
  acknowledged: number;
  /** Tokens that named no alarm, with the server's reason. */
  refusals: { token: string; reason: string }[];
}

/**
 * Acknowledges many alarms in one go. An empty list acknowledges nothing (the server never
 * reads it as "all"), so nothing is sent for one. A selection past the server's per-request
 * limit is sent in successive requests and the outcomes added up; a request that fails stops
 * the run, and what the earlier requests did stays done.
 */
export async function acknowledgeAlarms(tokens: string[]): Promise<BulkAcknowledgeOutcome> {
  const unique = [...new Set(tokens)];
  const outcome: BulkAcknowledgeOutcome = { acknowledged: 0, refusals: [] };
  for (let i = 0; i < unique.length; i += ACKNOWLEDGE_BATCH_LIMIT) {
    const data = await gql('device-management', ACKNOWLEDGE_ALARMS, {
      tokens: unique.slice(i, i + ACKNOWLEDGE_BATCH_LIMIT),
    });
    outcome.acknowledged += data.acknowledgeAlarms.acknowledged.length;
    outcome.refusals.push(...data.acknowledgeAlarms.refusals.map((r) => ({ token: r.token, reason: r.reason })));
  }
  return outcome;
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
    $originatorToken: String
    $state: String
    $severity: String
    $alarmKey: String
  ) {
    alarmStream(
      originatorType: $originatorType
      originatorToken: $originatorToken
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
      raisedTime
      occurredTime
    }
  }
`);
