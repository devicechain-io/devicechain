/* eslint-disable */
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
/** How a suppression window opens. */
export type AlarmSuppressionKind =
  /** A single bounded interval. */
  | 'ONE_SHOT'
  /** Weekly openings in an IANA time zone. */
  | 'RECURRING';

/** One population of devices to cover. */
export type AlarmSuppressionTargetInput = {
  /** Target token; omit for TENANT. */
  token?: string | null | undefined;
  /** DEVICE, DEVICE_TYPE, ENTITY_GROUP, AREA, ASSET, CUSTOMER or TENANT. */
  type: string;
};

/** Declare a paging hold. Creator identity and early-end fields are server-owned. */
export type AlarmSuppressionWindowCreateRequest = {
  /** Up to twenty alarm keys; empty or omitted matches every key. */
  alarmKeys?: Array<string> | null | undefined;
  /** Severity at or above which alarms bypass this window. */
  bypassSeverity?: string | null | undefined;
  /** Free-text description. */
  description?: string | null | undefined;
  /** Exclusive ONE_SHOT end, as an RFC 3339 timestamp; at most thirty-one days after start. */
  endTime?: string | null | undefined;
  /** Single interval or recurring schedule. */
  kind: AlarmSuppressionKind;
  /** Caller-defined metadata as JSON. */
  metadata?: string | null | undefined;
  /** Human-readable name. */
  name?: string | null | undefined;
  /** Required explanation for holding pages. */
  reason: string;
  /** RECURRING weekly slots as a JSON array of day, start and durationMinutes. */
  slots?: string | null | undefined;
  /** Inclusive ONE_SHOT start, as an RFC 3339 timestamp. */
  startTime?: string | null | undefined;
  /** One to twenty target populations. */
  targets: Array<AlarmSuppressionTargetInput>;
  /** IANA time zone for RECURRING openings. */
  timezone?: string | null | undefined;
  /** Stable identifier for the new window. */
  token: string;
  /** Inclusive recurrence bound, as an RFC 3339 timestamp. */
  validFrom?: string | null | undefined;
  /** Exclusive recurrence bound, as an RFC 3339 timestamp. */
  validUntil?: string | null | undefined;
};

/** Search visible whole windows before computing pagination totals. */
export type AlarmSuppressionWindowSearchCriteria = {
  /** One-based page number. */
  pageNumber: number;
  /** Maximum records per page. */
  pageSize: number;
  /** SCHEDULED, ACTIVE or ENDED; omit for every status. */
  status?: string | null | undefined;
  /** Match any target naming this token. */
  targetToken?: string | null | undefined;
};

/** Partial edit: omit to keep, null to clear nullable fields, value to replace. Required fields refuse null. */
export type AlarmSuppressionWindowUpdateRequest = {
  /** Replace all keys; null or an empty list matches every key. */
  alarmKeys?: Array<string> | null | undefined;
  /** Bypass severity; null disables bypass. */
  bypassSeverity?: string | null | undefined;
  /** Free-text description; null clears. */
  description?: string | null | undefined;
  /** ONE_SHOT end as RFC 3339; null clears only when the merged kind permits it. */
  endTime?: string | null | undefined;
  /** Schedule kind; null is refused. Change schedule fields together when switching kinds. */
  kind?: AlarmSuppressionKind | null | undefined;
  /** Metadata JSON; null clears. */
  metadata?: string | null | undefined;
  /** Human-readable name; null clears. */
  name?: string | null | undefined;
  /** Required explanation; null is refused. */
  reason?: string | null | undefined;
  /** RECURRING slots JSON; null clears only when the merged kind permits it. */
  slots?: string | null | undefined;
  /** ONE_SHOT start as RFC 3339; null clears only when the merged kind permits it. */
  startTime?: string | null | undefined;
  /** Replace all targets; null and empty lists are refused. */
  targets?: Array<AlarmSuppressionTargetInput> | null | undefined;
  /** RECURRING IANA time zone; null clears only when the merged kind permits it. */
  timezone?: string | null | undefined;
  /** Inclusive recurrence bound as RFC 3339; null clears. */
  validFrom?: string | null | undefined;
  /** Exclusive recurrence bound as RFC 3339; null clears. */
  validUntil?: string | null | undefined;
};

export type AlarmMaintenanceWindowFragment = { token: string, name: string | null, reason: string, description: string | null, alarmKeys: Array<string>, bypassSeverity: string | null, kind: AlarmSuppressionKind, startTime: string | null, endTime: string | null, timezone: string | null, slots: string | null, validFrom: string | null, validUntil: string | null, endedAt: string | null, status: string, updatedAt: string | null, targets: Array<{ type: string, token: string | null }>, openings: Array<{ start: string, end: string }> };

export type AlarmMaintenanceWindowsQueryVariables = Exact<{
  criteria: AlarmSuppressionWindowSearchCriteria;
}>;


export type AlarmMaintenanceWindowsQuery = { alarmSuppressionWindows: { results: Array<{ token: string, name: string | null, reason: string, description: string | null, alarmKeys: Array<string>, bypassSeverity: string | null, kind: AlarmSuppressionKind, startTime: string | null, endTime: string | null, timezone: string | null, slots: string | null, validFrom: string | null, validUntil: string | null, endedAt: string | null, status: string, updatedAt: string | null, targets: Array<{ type: string, token: string | null }>, openings: Array<{ start: string, end: string }> }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AlarmMaintenanceByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AlarmMaintenanceByTokenQuery = { alarmSuppressionWindowsByToken: Array<{ token: string, name: string | null, reason: string, description: string | null, alarmKeys: Array<string>, bypassSeverity: string | null, kind: AlarmSuppressionKind, startTime: string | null, endTime: string | null, timezone: string | null, slots: string | null, validFrom: string | null, validUntil: string | null, endedAt: string | null, status: string, updatedAt: string | null, targets: Array<{ type: string, token: string | null }>, openings: Array<{ start: string, end: string }> }> };

export type AlarmMaintenanceForDeviceQueryVariables = Exact<{
  deviceToken: string;
}>;


export type AlarmMaintenanceForDeviceQuery = { alarmSuppressionsForDevice: Array<{ token: string, name: string | null, reason: string, description: string | null, alarmKeys: Array<string>, bypassSeverity: string | null, kind: AlarmSuppressionKind, startTime: string | null, endTime: string | null, timezone: string | null, slots: string | null, validFrom: string | null, validUntil: string | null, endedAt: string | null, status: string, updatedAt: string | null, targets: Array<{ type: string, token: string | null }>, openings: Array<{ start: string, end: string }> }> };

export type AlarmMaintenanceStatusQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AlarmMaintenanceStatusQuery = { alarmSuppressionStatus: Array<{ alarmToken: string, suppression: { windowToken: string, until: string, window: { token: string, name: string | null, reason: string } | null } | null }> };

export type AlarmMaintenanceHistoryMarksQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AlarmMaintenanceHistoryMarksQuery = { alarmsByToken: Array<{ token: string, raisedDuringSuppression: string | null }> };

export type AlarmMaintenanceCreateMutationVariables = Exact<{
  request: AlarmSuppressionWindowCreateRequest;
}>;


export type AlarmMaintenanceCreateMutation = { createAlarmSuppressionWindow: { token: string } };

export type AlarmMaintenanceUpdateMutationVariables = Exact<{
  token: string;
  request: AlarmSuppressionWindowUpdateRequest;
}>;


export type AlarmMaintenanceUpdateMutation = { updateAlarmSuppressionWindow: { token: string } };

export type AlarmMaintenanceEndMutationVariables = Exact<{
  token: string;
}>;


export type AlarmMaintenanceEndMutation = { endAlarmSuppressionWindow: { token: string } };

export type AlarmMaintenanceDeleteMutationVariables = Exact<{
  token: string;
}>;


export type AlarmMaintenanceDeleteMutation = { deleteAlarmSuppressionWindow: boolean };

export type AlarmMaintenanceHistoryQueryVariables = Exact<{
  pageNumber: number;
  pageSize: number;
  state?: string | null | undefined;
  severity?: string | null | undefined;
  acknowledged?: boolean | null | undefined;
  raisedDuringSuppression: boolean;
}>;


export type AlarmMaintenanceHistoryQuery = { alarms: { results: Array<{ id: string, token: string, originatorType: string, originatorId: string, originatorToken: string | null, alarmKey: string, metricKey: string, state: string, acknowledged: boolean, severity: string, raisedTime: string | null, clearedTime: string | null, acknowledgedTime: string | null, acknowledgedBy: string | null, lastValue: number | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

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
export const AlarmMaintenanceWindowFragmentDoc = new TypedDocumentString(`
    fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {
  token
  name
  reason
  description
  targets {
    type
    token
  }
  alarmKeys
  bypassSeverity
  kind
  startTime
  endTime
  timezone
  slots
  validFrom
  validUntil
  endedAt
  status
  updatedAt
  openings(limit: 5) {
    start
    end
  }
}
    `, {"fragmentName":"AlarmMaintenanceWindow"}) as unknown as TypedDocumentString<AlarmMaintenanceWindowFragment, unknown>;
export const AlarmMaintenanceWindowsDocument = new TypedDocumentString(`
    query AlarmMaintenanceWindows($criteria: AlarmSuppressionWindowSearchCriteria!) {
  alarmSuppressionWindows(criteria: $criteria) {
    results {
      ...AlarmMaintenanceWindow
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {
  token
  name
  reason
  description
  targets {
    type
    token
  }
  alarmKeys
  bypassSeverity
  kind
  startTime
  endTime
  timezone
  slots
  validFrom
  validUntil
  endedAt
  status
  updatedAt
  openings(limit: 5) {
    start
    end
  }
}`) as unknown as TypedDocumentString<AlarmMaintenanceWindowsQuery, AlarmMaintenanceWindowsQueryVariables>;
export const AlarmMaintenanceByTokenDocument = new TypedDocumentString(`
    query AlarmMaintenanceByToken($tokens: [String!]!) {
  alarmSuppressionWindowsByToken(tokens: $tokens) {
    ...AlarmMaintenanceWindow
  }
}
    fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {
  token
  name
  reason
  description
  targets {
    type
    token
  }
  alarmKeys
  bypassSeverity
  kind
  startTime
  endTime
  timezone
  slots
  validFrom
  validUntil
  endedAt
  status
  updatedAt
  openings(limit: 5) {
    start
    end
  }
}`) as unknown as TypedDocumentString<AlarmMaintenanceByTokenQuery, AlarmMaintenanceByTokenQueryVariables>;
export const AlarmMaintenanceForDeviceDocument = new TypedDocumentString(`
    query AlarmMaintenanceForDevice($deviceToken: String!) {
  alarmSuppressionsForDevice(deviceToken: $deviceToken) {
    ...AlarmMaintenanceWindow
  }
}
    fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {
  token
  name
  reason
  description
  targets {
    type
    token
  }
  alarmKeys
  bypassSeverity
  kind
  startTime
  endTime
  timezone
  slots
  validFrom
  validUntil
  endedAt
  status
  updatedAt
  openings(limit: 5) {
    start
    end
  }
}`) as unknown as TypedDocumentString<AlarmMaintenanceForDeviceQuery, AlarmMaintenanceForDeviceQueryVariables>;
export const AlarmMaintenanceStatusDocument = new TypedDocumentString(`
    query AlarmMaintenanceStatus($tokens: [String!]!) {
  alarmSuppressionStatus(tokens: $tokens) {
    alarmToken
    suppression {
      windowToken
      until
      window {
        token
        name
        reason
      }
    }
  }
}
    `) as unknown as TypedDocumentString<AlarmMaintenanceStatusQuery, AlarmMaintenanceStatusQueryVariables>;
export const AlarmMaintenanceHistoryMarksDocument = new TypedDocumentString(`
    query AlarmMaintenanceHistoryMarks($tokens: [String!]!) {
  alarmsByToken(tokens: $tokens) {
    token
    raisedDuringSuppression
  }
}
    `) as unknown as TypedDocumentString<AlarmMaintenanceHistoryMarksQuery, AlarmMaintenanceHistoryMarksQueryVariables>;
export const AlarmMaintenanceCreateDocument = new TypedDocumentString(`
    mutation AlarmMaintenanceCreate($request: AlarmSuppressionWindowCreateRequest!) {
  createAlarmSuppressionWindow(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<AlarmMaintenanceCreateMutation, AlarmMaintenanceCreateMutationVariables>;
export const AlarmMaintenanceUpdateDocument = new TypedDocumentString(`
    mutation AlarmMaintenanceUpdate($token: String!, $request: AlarmSuppressionWindowUpdateRequest!) {
  updateAlarmSuppressionWindow(token: $token, request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<AlarmMaintenanceUpdateMutation, AlarmMaintenanceUpdateMutationVariables>;
export const AlarmMaintenanceEndDocument = new TypedDocumentString(`
    mutation AlarmMaintenanceEnd($token: String!) {
  endAlarmSuppressionWindow(token: $token) {
    token
  }
}
    `) as unknown as TypedDocumentString<AlarmMaintenanceEndMutation, AlarmMaintenanceEndMutationVariables>;
export const AlarmMaintenanceDeleteDocument = new TypedDocumentString(`
    mutation AlarmMaintenanceDelete($token: String!) {
  deleteAlarmSuppressionWindow(token: $token)
}
    `) as unknown as TypedDocumentString<AlarmMaintenanceDeleteMutation, AlarmMaintenanceDeleteMutationVariables>;
export const AlarmMaintenanceHistoryDocument = new TypedDocumentString(`
    query AlarmMaintenanceHistory($pageNumber: Int!, $pageSize: Int!, $state: String, $severity: String, $acknowledged: Boolean, $raisedDuringSuppression: Boolean!) {
  alarms(
    criteria: {
      pageNumber: $pageNumber
      pageSize: $pageSize
      state: $state
      severity: $severity
      acknowledged: $acknowledged
      raisedDuringSuppression: $raisedDuringSuppression
    }
  ) {
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
    `) as unknown as TypedDocumentString<AlarmMaintenanceHistoryQuery, AlarmMaintenanceHistoryQueryVariables>;