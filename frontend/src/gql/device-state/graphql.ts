/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DeviceStatesByDeviceIdQueryVariables = Exact<{
  deviceIds: Array<number> | number;
}>;


export type DeviceStatesByDeviceIdQuery = { deviceStatesByDeviceId: Array<{ id: string, deviceId: number, active: boolean, lastConnectTime: string | null, lastDisconnectTime: string | null, lastActivityTime: string | null, inactivityTimeout: number }> };

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

export const DeviceStatesByDeviceIdDocument = new TypedDocumentString(`
    query DeviceStatesByDeviceId($deviceIds: [Int!]!) {
  deviceStatesByDeviceId(deviceIds: $deviceIds) {
    id
    deviceId
    active
    lastConnectTime
    lastDisconnectTime
    lastActivityTime
    inactivityTimeout
  }
}
    `) as unknown as TypedDocumentString<DeviceStatesByDeviceIdQuery, DeviceStatesByDeviceIdQueryVariables>;