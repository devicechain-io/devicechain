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
        message
      }
      pagination {
        totalRecords
      }
    }
  }
` as unknown as TypedDocument<AlarmsQueryResult, AlarmsQueryVariables>;

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
}

export const ALARM_STREAM = `
  subscription DashboardAlarmStream(
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
      alarmToken
      eventType
    }
  }
` as unknown as TypedDocument<AlarmStreamResult, AlarmStreamVariables>;
