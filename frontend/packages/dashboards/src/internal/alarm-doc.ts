// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The typed alarm documents the hub's alarm channel drives, hand-authored.
//
// Packages carry no graphql-codegen (only apps do), so — like measurement-doc — these
// are written by hand and cast to TypedDocument. The alarm channel is query-then-
// reconcile (ADR-041): ALARMS_QUERY reads the authoritative raised-alarm rows
// (device-management, requires device:read), and ALARM_STREAM is a best-effort
// (at-most-once) trigger the hub debounces into a re-query — never the row source of
// truth. The trigger selects only what proves an event arrived; the rows come from the
// query.

import type { TypedDocument } from '@devicechain/client';

import type { AlarmRow } from '../types';

// ── Query (source of truth) ──────────────────────────────────────────────

export interface AlarmSearchCriteriaInput {
  pageNumber: number;
  pageSize: number;
  originatorType?: string | null;
  originator?: string | null;
  state?: string | null;
  severity?: string | null;
  acknowledged?: boolean | null;
  alarmKey?: string | null;
  // Only alarms raised on a device of this type (its token).
  deviceType?: string | null;
  // Only alarms whose span overlaps this range (RFC 3339, each optional).
  startTime?: string | null;
  endTime?: string | null;
  // Only alarms stamped with this customer, area or asset.
  anchor?: AlarmAnchorInput | null;
}

export interface AlarmAnchorInput {
  type: string;
  token: string;
}

export interface AlarmsQueryResult {
  alarms: {
    results: AlarmRow[];
    pagination: { totalRecords: number };
  };
}

export interface AlarmsQueryVariables {
  criteria: AlarmSearchCriteriaInput;
}

export const ALARMS_QUERY = `
  query DashboardAlarms($criteria: AlarmSearchCriteria!) {
    alarms(criteria: $criteria) {
      results {
        token
        originatorType
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
        totalRecords
      }
    }
  }
` as unknown as TypedDocument<AlarmsQueryResult, AlarmsQueryVariables>;

// ── Device types of a set of originators (one batched read) ─────────────
// The alarm row carries only its originator's token. A widget that groups alarms by device type
// resolves them with ONE devicesByToken read (up to 1000 tokens), never one query per alarm.

export interface AlarmDeviceTypesResult {
  devicesByToken: Array<{ token: string; deviceType: { token: string; name: string | null } }>;
}

export interface AlarmDeviceTypesVariables {
  tokens: string[];
}

export const ALARM_DEVICE_TYPES = `
  query DashboardAlarmDeviceTypes($tokens: [String!]!) {
    devicesByToken(tokens: $tokens) {
      token
      deviceType {
        token
        name
      }
    }
  }
` as unknown as TypedDocument<AlarmDeviceTypesResult, AlarmDeviceTypesVariables>;

// ── Alarm counts per device type (one grouped read) ─────────────────────
// The tab strip of an alarm table grouped by type: how many alarms match, per type and severity,
// counted by device-management, so the badges are exact however many alarms there are.

export type AlarmCountCriteriaInput = Omit<AlarmSearchCriteriaInput, 'pageNumber' | 'pageSize' | 'deviceType' | 'startTime' | 'endTime'>;

export interface AlarmTypeCountsResult {
  alarmCountsByDeviceType: Array<{ deviceType: { token: string; name: string | null }; severity: string; count: number }>;
}

export interface AlarmTypeCountsVariables {
  criteria: AlarmCountCriteriaInput;
}

export const ALARM_TYPE_COUNTS = `
  query DashboardAlarmTypeCounts($criteria: AlarmCountCriteria!) {
    alarmCountsByDeviceType(criteria: $criteria) {
      deviceType {
        token
        name
      }
      severity
      count
    }
  }
` as unknown as TypedDocument<AlarmTypeCountsResult, AlarmTypeCountsVariables>;

// ── Alarm counts per severity (one grouped read) ────────────────────────
// A severity breakdown. With no deviceType it counts every alarm the search would return, whatever
// its originator, so the counts sum to the alarm search's total; with one, only that type's devices.

export interface AlarmSeverityCountsResult {
  alarmCountsBySeverity: Array<{ severity: string; count: number }>;
}

export interface AlarmSeverityCountsVariables {
  criteria: AlarmCountCriteriaInput;
  deviceType: string | null;
}

export const ALARM_SEVERITY_COUNTS = `
  query DashboardAlarmSeverityCounts($criteria: AlarmCountCriteria!, $deviceType: String) {
    alarmCountsBySeverity(criteria: $criteria, deviceType: $deviceType) {
      severity
      count
    }
  }
` as unknown as TypedDocument<AlarmSeverityCountsResult, AlarmSeverityCountsVariables>;

// ── Live trigger ─────────────────────────────────────────────────────────

export interface AlarmStreamResult {
  alarmStream: { alarmToken: string; eventType: string };
}

export interface AlarmStreamVariables {
  originatorType?: string | null;
  originator?: string | null;
  state?: string | null;
  severity?: string | null;
  alarmKey?: string | null;
  anchor?: AlarmAnchorInput | null;
}

export const ALARM_STREAM = `
  subscription DashboardAlarmStream(
    $originatorType: String
    $originator: String
    $state: String
    $severity: String
    $alarmKey: String
    $anchor: AlarmAnchor
  ) {
    alarmStream(
      originatorType: $originatorType
      originator: $originator
      state: $state
      severity: $severity
      alarmKey: $alarmKey
      anchor: $anchor
    ) {
      alarmToken
      eventType
    }
  }
` as unknown as TypedDocument<AlarmStreamResult, AlarmStreamVariables>;

// ── Operator actions (writes) ────────────────────────────────────────────
// acknowledge/clear an alarm by token (device-management, require alarm:write). The
// acknowledging identity is taken server-side from the authenticated subject; the
// caller supplies only the token. Only the token is selected back — the hub reconciles
// the rows via the query afterward.

export interface AlarmMutationVariables {
  token: string;
}

export interface AlarmMutationResult {
  [key: string]: { token: string };
}

export const ACKNOWLEDGE_ALARM = `
  mutation DashboardAcknowledgeAlarm($token: String!) {
    acknowledgeAlarm(token: $token) {
      token
    }
  }
` as unknown as TypedDocument<AlarmMutationResult, AlarmMutationVariables>;

export const CLEAR_ALARM = `
  mutation DashboardClearAlarm($token: String!) {
    clearAlarm(token: $token) {
      token
    }
  }
` as unknown as TypedDocument<AlarmMutationResult, AlarmMutationVariables>;
