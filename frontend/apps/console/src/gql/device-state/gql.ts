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
    "\n  query DeviceStatesByDeviceToken($deviceTokens: [String!]!) {\n    deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {\n      id\n      deviceToken\n      active\n      lastConnectTime\n      lastDisconnectTime\n      lastActivityTime\n      presenceSource\n      presenceTime\n      sessionId\n    }\n  }\n": typeof types.DeviceStatesByDeviceTokenDocument,
    "\n  query LatestMeasurements($deviceToken: String!) {\n    latestMeasurements(deviceToken: $deviceToken) {\n      id\n      name\n      value\n      occurredTime\n    }\n  }\n": typeof types.LatestMeasurementsDocument,
    "\n  query DroppedMeasurements($deviceToken: String!) {\n    droppedMeasurements(deviceToken: $deviceToken) {\n      id\n      name\n      reason\n      lastValue\n      count\n      lastTime\n    }\n  }\n": typeof types.DroppedMeasurementsDocument,
    "\n  query LatestLocation($deviceToken: String!) {\n    latestLocation(deviceToken: $deviceToken) {\n      id\n      deviceToken\n      latitude\n      longitude\n      elevation\n      accuracy\n      speed\n      heading\n      occurredTime\n    }\n  }\n": typeof types.LatestLocationDocument,
    "\n  query DeviceStateCount($criteria: DeviceStateSearchCriteria!) {\n    deviceStates(criteria: $criteria) {\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": typeof types.DeviceStateCountDocument,
};
const documents: Documents = {
    "\n  query DeviceStatesByDeviceToken($deviceTokens: [String!]!) {\n    deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {\n      id\n      deviceToken\n      active\n      lastConnectTime\n      lastDisconnectTime\n      lastActivityTime\n      presenceSource\n      presenceTime\n      sessionId\n    }\n  }\n": types.DeviceStatesByDeviceTokenDocument,
    "\n  query LatestMeasurements($deviceToken: String!) {\n    latestMeasurements(deviceToken: $deviceToken) {\n      id\n      name\n      value\n      occurredTime\n    }\n  }\n": types.LatestMeasurementsDocument,
    "\n  query DroppedMeasurements($deviceToken: String!) {\n    droppedMeasurements(deviceToken: $deviceToken) {\n      id\n      name\n      reason\n      lastValue\n      count\n      lastTime\n    }\n  }\n": types.DroppedMeasurementsDocument,
    "\n  query LatestLocation($deviceToken: String!) {\n    latestLocation(deviceToken: $deviceToken) {\n      id\n      deviceToken\n      latitude\n      longitude\n      elevation\n      accuracy\n      speed\n      heading\n      occurredTime\n    }\n  }\n": types.LatestLocationDocument,
    "\n  query DeviceStateCount($criteria: DeviceStateSearchCriteria!) {\n    deviceStates(criteria: $criteria) {\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": types.DeviceStateCountDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceStatesByDeviceToken($deviceTokens: [String!]!) {\n    deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {\n      id\n      deviceToken\n      active\n      lastConnectTime\n      lastDisconnectTime\n      lastActivityTime\n      presenceSource\n      presenceTime\n      sessionId\n    }\n  }\n"): typeof import('./graphql').DeviceStatesByDeviceTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query LatestMeasurements($deviceToken: String!) {\n    latestMeasurements(deviceToken: $deviceToken) {\n      id\n      name\n      value\n      occurredTime\n    }\n  }\n"): typeof import('./graphql').LatestMeasurementsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DroppedMeasurements($deviceToken: String!) {\n    droppedMeasurements(deviceToken: $deviceToken) {\n      id\n      name\n      reason\n      lastValue\n      count\n      lastTime\n    }\n  }\n"): typeof import('./graphql').DroppedMeasurementsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query LatestLocation($deviceToken: String!) {\n    latestLocation(deviceToken: $deviceToken) {\n      id\n      deviceToken\n      latitude\n      longitude\n      elevation\n      accuracy\n      speed\n      heading\n      occurredTime\n    }\n  }\n"): typeof import('./graphql').LatestLocationDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceStateCount($criteria: DeviceStateSearchCriteria!) {\n    deviceStates(criteria: $criteria) {\n      pagination {\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceStateCountDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
