/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DeviceCreateRequest = {
  description?: string | null | undefined;
  deviceTypeToken: string;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type DeviceSearchCriteria = {
  deviceType?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type DeviceTypeCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type DeviceTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type DevicesQueryVariables = Exact<{
  criteria: DeviceSearchCriteria;
}>;


export type DevicesQuery = { devices: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type DeviceByTokenQuery = { devicesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }> };

export type CreateDeviceMutationVariables = Exact<{
  request?: DeviceCreateRequest | null | undefined;
}>;


export type CreateDeviceMutation = { createDevice: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type UpdateDeviceMutationVariables = Exact<{
  token: string;
  request?: DeviceCreateRequest | null | undefined;
}>;


export type UpdateDeviceMutation = { updateDevice: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type DeleteDeviceMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceMutation = { deleteDevice: boolean };

export type DeviceTypesQueryVariables = Exact<{
  criteria: DeviceTypeSearchCriteria;
}>;


export type DeviceTypesQuery = { deviceTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type DeviceTypeByTokenQuery = { deviceTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateDeviceTypeMutationVariables = Exact<{
  request?: DeviceTypeCreateRequest | null | undefined;
}>;


export type CreateDeviceTypeMutation = { createDeviceType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateDeviceTypeMutationVariables = Exact<{
  token: string;
  request?: DeviceTypeCreateRequest | null | undefined;
}>;


export type UpdateDeviceTypeMutation = { updateDeviceType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteDeviceTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceTypeMutation = { deleteDeviceType: boolean };

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

export const DevicesDocument = new TypedDocumentString(`
    query Devices($criteria: DeviceSearchCriteria!) {
  devices(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      deviceType {
        id
        token
        name
        backgroundColor
        foregroundColor
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DevicesQuery, DevicesQueryVariables>;
export const DeviceByTokenDocument = new TypedDocumentString(`
    query DeviceByToken($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    deviceType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceByTokenQuery, DeviceByTokenQueryVariables>;
export const CreateDeviceDocument = new TypedDocumentString(`
    mutation CreateDevice($request: DeviceCreateRequest) {
  createDevice(request: $request) {
    id
    token
    name
    description
    createdAt
    deviceType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceMutation, CreateDeviceMutationVariables>;
export const UpdateDeviceDocument = new TypedDocumentString(`
    mutation UpdateDevice($token: String!, $request: DeviceCreateRequest) {
  updateDevice(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    deviceType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateDeviceMutation, UpdateDeviceMutationVariables>;
export const DeleteDeviceDocument = new TypedDocumentString(`
    mutation DeleteDevice($token: String!) {
  deleteDevice(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceMutation, DeleteDeviceMutationVariables>;
export const DeviceTypesDocument = new TypedDocumentString(`
    query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {
  deviceTypes(criteria: $criteria) {
    results {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceTypesQuery, DeviceTypesQueryVariables>;
export const DeviceTypeByTokenDocument = new TypedDocumentString(`
    query DeviceTypeByToken($tokens: [String!]!) {
  deviceTypesByToken(tokens: $tokens) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<DeviceTypeByTokenQuery, DeviceTypeByTokenQueryVariables>;
export const CreateDeviceTypeDocument = new TypedDocumentString(`
    mutation CreateDeviceType($request: DeviceTypeCreateRequest) {
  createDeviceType(request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceTypeMutation, CreateDeviceTypeMutationVariables>;
export const UpdateDeviceTypeDocument = new TypedDocumentString(`
    mutation UpdateDeviceType($token: String!, $request: DeviceTypeCreateRequest) {
  updateDeviceType(token: $token, request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateDeviceTypeMutation, UpdateDeviceTypeMutationVariables>;
export const DeleteDeviceTypeDocument = new TypedDocumentString(`
    mutation DeleteDeviceType($token: String!) {
  deleteDeviceType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceTypeMutation, DeleteDeviceTypeMutationVariables>;