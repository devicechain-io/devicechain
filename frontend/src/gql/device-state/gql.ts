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
    "\n  query DeviceStatesByDeviceId($deviceIds: [Int!]!) {\n    deviceStatesByDeviceId(deviceIds: $deviceIds) {\n      id\n      deviceId\n      active\n      lastConnectTime\n      lastDisconnectTime\n      lastActivityTime\n      inactivityTimeout\n    }\n  }\n": typeof types.DeviceStatesByDeviceIdDocument,
    "\n  query LatestMeasurements($deviceId: Int!) {\n    latestMeasurements(deviceId: $deviceId) {\n      id\n      name\n      value\n      occurredTime\n    }\n  }\n": typeof types.LatestMeasurementsDocument,
};
const documents: Documents = {
    "\n  query DeviceStatesByDeviceId($deviceIds: [Int!]!) {\n    deviceStatesByDeviceId(deviceIds: $deviceIds) {\n      id\n      deviceId\n      active\n      lastConnectTime\n      lastDisconnectTime\n      lastActivityTime\n      inactivityTimeout\n    }\n  }\n": types.DeviceStatesByDeviceIdDocument,
    "\n  query LatestMeasurements($deviceId: Int!) {\n    latestMeasurements(deviceId: $deviceId) {\n      id\n      name\n      value\n      occurredTime\n    }\n  }\n": types.LatestMeasurementsDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceStatesByDeviceId($deviceIds: [Int!]!) {\n    deviceStatesByDeviceId(deviceIds: $deviceIds) {\n      id\n      deviceId\n      active\n      lastConnectTime\n      lastDisconnectTime\n      lastActivityTime\n      inactivityTimeout\n    }\n  }\n"): typeof import('./graphql').DeviceStatesByDeviceIdDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query LatestMeasurements($deviceId: Int!) {\n    latestMeasurements(deviceId: $deviceId) {\n      id\n      name\n      value\n      occurredTime\n    }\n  }\n"): typeof import('./graphql').LatestMeasurementsDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
