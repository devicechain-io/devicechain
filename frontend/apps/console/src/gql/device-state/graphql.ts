/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DeviceStatesByDeviceTokenQueryVariables = Exact<{
  deviceTokens: Array<string> | string;
}>;


export type DeviceStatesByDeviceTokenQuery = { deviceStatesByDeviceToken: Array<{ id: string, deviceToken: string, active: boolean, lastConnectTime: string | null, lastDisconnectTime: string | null, lastActivityTime: string | null, inactivityTimeout: number }> };

export type LatestMeasurementsQueryVariables = Exact<{
  deviceToken: string;
}>;


export type LatestMeasurementsQuery = { latestMeasurements: Array<{ id: string, name: string, value: number | null, occurredTime: string }> };

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