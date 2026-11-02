/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/**
 * Why a measurement key sent by a device is discarded while the rest of its message is stored.
 * In every case the remedy is the same: declare the key as a numeric metric on the device's
 * profile to store it.
 */
export type DroppedMeasurementReason =
  /** The key is declared on the profile with a data type that is not a time-series metric. */
  | 'DECLARED_NOT_STORABLE'
  /** The key is not declared on the profile and its value is not a number, for example the text "ok". */
  | 'UNDECLARED_NON_NUMERIC'
  /**
   * The key is not declared on the profile and its value is a number that cannot be stored: not
   * a number (NaN), infinite, or beyond the range of a stored measurement.
   */
  | 'UNDECLARED_UNSTORABLE';

export type DeviceStatesByDeviceTokenQueryVariables = Exact<{
  deviceTokens: Array<string> | string;
}>;


export type DeviceStatesByDeviceTokenQuery = { deviceStatesByDeviceToken: Array<{ id: string, deviceToken: string, active: boolean, lastConnectTime: string | null, lastDisconnectTime: string | null, lastActivityTime: string | null, inactivityTimeout: number, presenceSource: string }> };

export type LatestMeasurementsQueryVariables = Exact<{
  deviceToken: string;
}>;


export type LatestMeasurementsQuery = { latestMeasurements: Array<{ id: string, name: string, value: number | null, occurredTime: string }> };

export type DroppedMeasurementsQueryVariables = Exact<{
  deviceToken: string;
}>;


export type DroppedMeasurementsQuery = { droppedMeasurements: Array<{ id: string, name: string, reason: DroppedMeasurementReason, lastValue: string, count: number, lastTime: string }> };

export type LatestLocationQueryVariables = Exact<{
  deviceToken: string;
}>;


export type LatestLocationQuery = { latestLocation: { id: string, deviceToken: string, latitude: number | null, longitude: number | null, elevation: number | null, accuracy: number | null, speed: number | null, heading: number | null, occurredTime: string } | null };

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

export const DeviceStatesByDeviceTokenDocument = new TypedDocumentString(`
    query DeviceStatesByDeviceToken($deviceTokens: [String!]!) {
  deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
    id
    deviceToken
    active
    lastConnectTime
    lastDisconnectTime
    lastActivityTime
    inactivityTimeout
    presenceSource
  }
}
    `) as unknown as TypedDocumentString<DeviceStatesByDeviceTokenQuery, DeviceStatesByDeviceTokenQueryVariables>;
export const LatestMeasurementsDocument = new TypedDocumentString(`
    query LatestMeasurements($deviceToken: String!) {
  latestMeasurements(deviceToken: $deviceToken) {
    id
    name
    value
    occurredTime
  }
}
    `) as unknown as TypedDocumentString<LatestMeasurementsQuery, LatestMeasurementsQueryVariables>;
export const DroppedMeasurementsDocument = new TypedDocumentString(`
    query DroppedMeasurements($deviceToken: String!) {
  droppedMeasurements(deviceToken: $deviceToken) {
    id
    name
    reason
    lastValue
    count
    lastTime
  }
}
    `) as unknown as TypedDocumentString<DroppedMeasurementsQuery, DroppedMeasurementsQueryVariables>;
export const LatestLocationDocument = new TypedDocumentString(`
    query LatestLocation($deviceToken: String!) {
  latestLocation(deviceToken: $deviceToken) {
    id
    deviceToken
    latitude
    longitude
    elevation
    accuracy
    speed
    heading
    occurredTime
  }
}
    `) as unknown as TypedDocumentString<LatestLocationQuery, LatestLocationQueryVariables>;