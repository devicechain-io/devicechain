// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/alarm-maintenance';
import { optionalSuppression } from '@/lib/optional-suppression';
import type {
  AlarmMaintenanceWindowFragment,
  AlarmMaintenanceStatusQuery,
  AlarmMaintenanceHistoryMarksQuery,
  AlarmSuppressionWindowCreateRequest,
  AlarmSuppressionWindowUpdateRequest,
  AlarmSuppressionWindowSearchCriteria,
} from '@/gql/alarm-maintenance/graphql';

export type MaintenanceWindow = AlarmMaintenanceWindowFragment;
export type Suppression = NonNullable<AlarmMaintenanceStatusQuery['alarmSuppressionStatus'][number]['suppression']>;
export type { AlarmSuppressionWindowCreateRequest, AlarmSuppressionWindowUpdateRequest };

graphql(`
  fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {
    token name reason description targets { type token } alarmKeys bypassSeverity kind
    startTime endTime timezone slots validFrom validUntil endedAt status updatedAt
    openings(limit: 5) { start end }
  }
`);
const LIST = graphql(`
  query AlarmMaintenanceWindows($criteria: AlarmSuppressionWindowSearchCriteria!) {
    alarmSuppressionWindows(criteria: $criteria) {
      results { ...AlarmMaintenanceWindow }
      pagination { pageStart pageEnd totalRecords }
    }
  }
`);
export const listWindows = (criteria: AlarmSuppressionWindowSearchCriteria) => optionalSuppression(async () =>
  (await gql('device-management', LIST, { criteria })).alarmSuppressionWindows);

const GET = graphql(`
  query AlarmMaintenanceByToken($tokens: [String!]!) {
    alarmSuppressionWindowsByToken(tokens: $tokens) { ...AlarmMaintenanceWindow }
  }
`);
export const getWindow = (token: string) => optionalSuppression(async () =>
  (await gql('device-management', GET, { tokens: [token] })).alarmSuppressionWindowsByToken[0] ?? null);

const DEVICE = graphql(`
  query AlarmMaintenanceForDevice($deviceToken: String!) {
    alarmSuppressionsForDevice(deviceToken: $deviceToken) { ...AlarmMaintenanceWindow }
  }
`);
export const deviceWindows = (deviceToken: string) => optionalSuppression(async () =>
  (await gql('device-management', DEVICE, { deviceToken })).alarmSuppressionsForDevice);

const STATUS = graphql(`
  query AlarmMaintenanceStatus($tokens: [String!]!) {
    alarmSuppressionStatus(tokens: $tokens) {
      alarmToken suppression { windowToken until window { token name reason } }
    }
  }
`);
/** Never alter the alarm documents. Deduplicate and respect the SDL's 200-token cap. */
export const suppressionStatus = (tokens: string[]) => optionalSuppression(async () => {
  const unique = [...new Set(tokens)];
  const rows: AlarmMaintenanceStatusQuery['alarmSuppressionStatus'] = [];
  for (let i = 0; i < unique.length; i += 200) {
    rows.push(...(await gql('device-management', STATUS, { tokens: unique.slice(i, i + 200) })).alarmSuppressionStatus);
  }
  return rows;
});
const HISTORY_MARKS = graphql(`
  query AlarmMaintenanceHistoryMarks($tokens: [String!]!) {
    alarmsByToken(tokens: $tokens) { token raisedDuringSuppression }
  }
`);
/** Separate optional document; keep batches bounded like the live status read. */
export const suppressionHistoryMarks = (tokens: string[]) => optionalSuppression(async () => {
  const unique = [...new Set(tokens)];
  const rows: AlarmMaintenanceHistoryMarksQuery['alarmsByToken'] = [];
  for (let i = 0; i < unique.length; i += 200) {
    rows.push(...(await gql('device-management', HISTORY_MARKS, { tokens: unique.slice(i, i + 200) })).alarmsByToken);
  }
  return rows;
});
const CREATE = graphql(`
  mutation AlarmMaintenanceCreate($request: AlarmSuppressionWindowCreateRequest!) {
    createAlarmSuppressionWindow(request: $request) { token }
  }
`);
export const createWindow = async (request: AlarmSuppressionWindowCreateRequest) =>
  (await gql('device-management', CREATE, { request })).createAlarmSuppressionWindow;
const UPDATE = graphql(`
  mutation AlarmMaintenanceUpdate($token: String!, $request: AlarmSuppressionWindowUpdateRequest!) {
    updateAlarmSuppressionWindow(token: $token, request: $request) { token }
  }
`);
export const updateWindow = (token: string, request: AlarmSuppressionWindowUpdateRequest) =>
  gql('device-management', UPDATE, { token, request });
const END = graphql(`
  mutation AlarmMaintenanceEnd($token: String!) {
    endAlarmSuppressionWindow(token: $token) { token }
  }
`);
export const endWindow = (token: string) => gql('device-management', END, { token });
const DELETE = graphql(`
  mutation AlarmMaintenanceDelete($token: String!) { deleteAlarmSuppressionWindow(token: $token) }
`);
export const deleteWindow = (token: string) => gql('device-management', DELETE, { token });

// Separate document: even the new input member is absent from the legacy alarm request.
const HISTORY = graphql(`
  query AlarmMaintenanceHistory($pageNumber: Int!, $pageSize: Int!, $state: String, $severity: String, $acknowledged: Boolean, $raisedDuringSuppression: Boolean!) {
    alarms(criteria: { pageNumber: $pageNumber, pageSize: $pageSize, state: $state, severity: $severity, acknowledged: $acknowledged, raisedDuringSuppression: $raisedDuringSuppression }) {
      results {
        id token originatorType originatorId originatorToken alarmKey metricKey state acknowledged severity
        raisedTime clearedTime acknowledgedTime acknowledgedBy lastValue
      }
      pagination { pageStart pageEnd totalRecords }
    }
  }
`);
export const listHistoryAlarms = (options: {
  pageNumber: number; pageSize: number; state?: string; severity?: string; acknowledged?: boolean;
}, raisedDuringSuppression: boolean) => optionalSuppression(async () =>
  (await gql('device-management', HISTORY, { ...options, raisedDuringSuppression })).alarms);
