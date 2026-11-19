/* eslint-disable */
import * as types from './graphql';



/**
 * Map of all GraphQL operations in the project.
 *
 * This map has several performance disadvantages:
 * 1. It is not tree-shakeable, so it will include all operations in the project.
 * 2. It is not minifiable, so the string of a GraphQL query will be multiple times inside the bundle.
 * 3. It does not support dead code elimination, so it will add unused operations.
 *
 * Therefore it is highly recommended to use the babel or swc plugin for production.
 * Learn more about it here: https://the-guild.dev/graphql/codegen/plugins/presets/preset-client#reducing-bundle-size
 */
type Documents = {
    "\n  fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {\n    token name reason description targets { type token } alarmKeys bypassSeverity kind\n    startTime endTime timezone slots validFrom validUntil endedAt status updatedAt\n    openings(limit: 5) { start end }\n  }\n": typeof types.AlarmMaintenanceWindowFragmentDoc,
    "\n  query AlarmMaintenanceWindows($criteria: AlarmSuppressionWindowSearchCriteria!) {\n    alarmSuppressionWindows(criteria: $criteria) {\n      results { ...AlarmMaintenanceWindow }\n      pagination { pageStart pageEnd totalRecords }\n    }\n  }\n": typeof types.AlarmMaintenanceWindowsDocument,
    "\n  query AlarmMaintenanceByToken($tokens: [String!]!) {\n    alarmSuppressionWindowsByToken(tokens: $tokens) { ...AlarmMaintenanceWindow }\n  }\n": typeof types.AlarmMaintenanceByTokenDocument,
    "\n  query AlarmMaintenanceForDevice($deviceToken: String!) {\n    alarmSuppressionsForDevice(deviceToken: $deviceToken) { ...AlarmMaintenanceWindow }\n  }\n": typeof types.AlarmMaintenanceForDeviceDocument,
    "\n  query AlarmMaintenanceStatus($tokens: [String!]!) {\n    alarmSuppressionStatus(tokens: $tokens) {\n      alarmToken suppression { windowToken until window { token name reason } }\n    }\n  }\n": typeof types.AlarmMaintenanceStatusDocument,
    "\n  query AlarmMaintenanceHistoryMarks($tokens: [String!]!) {\n    alarmsByToken(tokens: $tokens) { token raisedDuringSuppression }\n  }\n": typeof types.AlarmMaintenanceHistoryMarksDocument,
    "\n  mutation AlarmMaintenanceCreate($request: AlarmSuppressionWindowCreateRequest!) {\n    createAlarmSuppressionWindow(request: $request) { token }\n  }\n": typeof types.AlarmMaintenanceCreateDocument,
    "\n  mutation AlarmMaintenanceUpdate($token: String!, $request: AlarmSuppressionWindowUpdateRequest!) {\n    updateAlarmSuppressionWindow(token: $token, request: $request) { token }\n  }\n": typeof types.AlarmMaintenanceUpdateDocument,
    "\n  mutation AlarmMaintenanceEnd($token: String!) {\n    endAlarmSuppressionWindow(token: $token) { token }\n  }\n": typeof types.AlarmMaintenanceEndDocument,
    "\n  mutation AlarmMaintenanceDelete($token: String!) { deleteAlarmSuppressionWindow(token: $token) }\n": typeof types.AlarmMaintenanceDeleteDocument,
    "\n  query AlarmMaintenanceHistory($pageNumber: Int!, $pageSize: Int!, $state: String, $severity: String, $acknowledged: Boolean, $raisedDuringSuppression: Boolean!) {\n    alarms(criteria: { pageNumber: $pageNumber, pageSize: $pageSize, state: $state, severity: $severity, acknowledged: $acknowledged, raisedDuringSuppression: $raisedDuringSuppression }) {\n      results {\n        id token originatorType originatorId originatorToken alarmKey metricKey state acknowledged severity\n        raisedTime clearedTime acknowledgedTime acknowledgedBy lastValue\n      }\n      pagination { pageStart pageEnd totalRecords }\n    }\n  }\n": typeof types.AlarmMaintenanceHistoryDocument,
};
const documents: Documents = {
    "\n  fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {\n    token name reason description targets { type token } alarmKeys bypassSeverity kind\n    startTime endTime timezone slots validFrom validUntil endedAt status updatedAt\n    openings(limit: 5) { start end }\n  }\n": types.AlarmMaintenanceWindowFragmentDoc,
    "\n  query AlarmMaintenanceWindows($criteria: AlarmSuppressionWindowSearchCriteria!) {\n    alarmSuppressionWindows(criteria: $criteria) {\n      results { ...AlarmMaintenanceWindow }\n      pagination { pageStart pageEnd totalRecords }\n    }\n  }\n": types.AlarmMaintenanceWindowsDocument,
    "\n  query AlarmMaintenanceByToken($tokens: [String!]!) {\n    alarmSuppressionWindowsByToken(tokens: $tokens) { ...AlarmMaintenanceWindow }\n  }\n": types.AlarmMaintenanceByTokenDocument,
    "\n  query AlarmMaintenanceForDevice($deviceToken: String!) {\n    alarmSuppressionsForDevice(deviceToken: $deviceToken) { ...AlarmMaintenanceWindow }\n  }\n": types.AlarmMaintenanceForDeviceDocument,
    "\n  query AlarmMaintenanceStatus($tokens: [String!]!) {\n    alarmSuppressionStatus(tokens: $tokens) {\n      alarmToken suppression { windowToken until window { token name reason } }\n    }\n  }\n": types.AlarmMaintenanceStatusDocument,
    "\n  query AlarmMaintenanceHistoryMarks($tokens: [String!]!) {\n    alarmsByToken(tokens: $tokens) { token raisedDuringSuppression }\n  }\n": types.AlarmMaintenanceHistoryMarksDocument,
    "\n  mutation AlarmMaintenanceCreate($request: AlarmSuppressionWindowCreateRequest!) {\n    createAlarmSuppressionWindow(request: $request) { token }\n  }\n": types.AlarmMaintenanceCreateDocument,
    "\n  mutation AlarmMaintenanceUpdate($token: String!, $request: AlarmSuppressionWindowUpdateRequest!) {\n    updateAlarmSuppressionWindow(token: $token, request: $request) { token }\n  }\n": types.AlarmMaintenanceUpdateDocument,
    "\n  mutation AlarmMaintenanceEnd($token: String!) {\n    endAlarmSuppressionWindow(token: $token) { token }\n  }\n": types.AlarmMaintenanceEndDocument,
    "\n  mutation AlarmMaintenanceDelete($token: String!) { deleteAlarmSuppressionWindow(token: $token) }\n": types.AlarmMaintenanceDeleteDocument,
    "\n  query AlarmMaintenanceHistory($pageNumber: Int!, $pageSize: Int!, $state: String, $severity: String, $acknowledged: Boolean, $raisedDuringSuppression: Boolean!) {\n    alarms(criteria: { pageNumber: $pageNumber, pageSize: $pageSize, state: $state, severity: $severity, acknowledged: $acknowledged, raisedDuringSuppression: $raisedDuringSuppression }) {\n      results {\n        id token originatorType originatorId originatorToken alarmKey metricKey state acknowledged severity\n        raisedTime clearedTime acknowledgedTime acknowledgedBy lastValue\n      }\n      pagination { pageStart pageEnd totalRecords }\n    }\n  }\n": types.AlarmMaintenanceHistoryDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  fragment AlarmMaintenanceWindow on AlarmSuppressionWindow {\n    token name reason description targets { type token } alarmKeys bypassSeverity kind\n    startTime endTime timezone slots validFrom validUntil endedAt status updatedAt\n    openings(limit: 5) { start end }\n  }\n"): typeof import('./graphql').AlarmMaintenanceWindowFragmentDoc;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AlarmMaintenanceWindows($criteria: AlarmSuppressionWindowSearchCriteria!) {\n    alarmSuppressionWindows(criteria: $criteria) {\n      results { ...AlarmMaintenanceWindow }\n      pagination { pageStart pageEnd totalRecords }\n    }\n  }\n"): typeof import('./graphql').AlarmMaintenanceWindowsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AlarmMaintenanceByToken($tokens: [String!]!) {\n    alarmSuppressionWindowsByToken(tokens: $tokens) { ...AlarmMaintenanceWindow }\n  }\n"): typeof import('./graphql').AlarmMaintenanceByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AlarmMaintenanceForDevice($deviceToken: String!) {\n    alarmSuppressionsForDevice(deviceToken: $deviceToken) { ...AlarmMaintenanceWindow }\n  }\n"): typeof import('./graphql').AlarmMaintenanceForDeviceDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AlarmMaintenanceStatus($tokens: [String!]!) {\n    alarmSuppressionStatus(tokens: $tokens) {\n      alarmToken suppression { windowToken until window { token name reason } }\n    }\n  }\n"): typeof import('./graphql').AlarmMaintenanceStatusDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AlarmMaintenanceHistoryMarks($tokens: [String!]!) {\n    alarmsByToken(tokens: $tokens) { token raisedDuringSuppression }\n  }\n"): typeof import('./graphql').AlarmMaintenanceHistoryMarksDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AlarmMaintenanceCreate($request: AlarmSuppressionWindowCreateRequest!) {\n    createAlarmSuppressionWindow(request: $request) { token }\n  }\n"): typeof import('./graphql').AlarmMaintenanceCreateDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AlarmMaintenanceUpdate($token: String!, $request: AlarmSuppressionWindowUpdateRequest!) {\n    updateAlarmSuppressionWindow(token: $token, request: $request) { token }\n  }\n"): typeof import('./graphql').AlarmMaintenanceUpdateDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AlarmMaintenanceEnd($token: String!) {\n    endAlarmSuppressionWindow(token: $token) { token }\n  }\n"): typeof import('./graphql').AlarmMaintenanceEndDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AlarmMaintenanceDelete($token: String!) { deleteAlarmSuppressionWindow(token: $token) }\n"): typeof import('./graphql').AlarmMaintenanceDeleteDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AlarmMaintenanceHistory($pageNumber: Int!, $pageSize: Int!, $state: String, $severity: String, $acknowledged: Boolean, $raisedDuringSuppression: Boolean!) {\n    alarms(criteria: { pageNumber: $pageNumber, pageSize: $pageSize, state: $state, severity: $severity, acknowledged: $acknowledged, raisedDuringSuppression: $raisedDuringSuppression }) {\n      results {\n        id token originatorType originatorId originatorToken alarmKey metricKey state acknowledged severity\n        raisedTime clearedTime acknowledgedTime acknowledgedBy lastValue\n      }\n      pagination { pageStart pageEnd totalRecords }\n    }\n  }\n"): typeof import('./graphql').AlarmMaintenanceHistoryDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
