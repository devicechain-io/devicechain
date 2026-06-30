/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type AreaCreateRequest = {
  areaTypeToken: string;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AreaSearchCriteria = {
  areaTypeToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type AreaTypeCreateRequest = {
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

export type AreaTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type AssetCreateRequest = {
  assetTypeToken: string;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AssetSearchCriteria = {
  assetTypeToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type AssetTypeCreateRequest = {
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

export type AssetTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type CustomerCreateRequest = {
  customerTypeToken: string;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type CustomerSearchCriteria = {
  customerTypeToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type CustomerTypeCreateRequest = {
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

export type CustomerTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

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

export type AreasQueryVariables = Exact<{
  criteria: AreaSearchCriteria;
}>;


export type AreasQuery = { areas: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AreaByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AreaByTokenQuery = { areasByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }> };

export type CreateAreaMutationVariables = Exact<{
  request?: AreaCreateRequest | null | undefined;
}>;


export type CreateAreaMutation = { createArea: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type UpdateAreaMutationVariables = Exact<{
  token: string;
  request?: AreaCreateRequest | null | undefined;
}>;


export type UpdateAreaMutation = { updateArea: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type DeleteAreaMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAreaMutation = { deleteArea: boolean };

export type AreaTypesQueryVariables = Exact<{
  criteria: AreaTypeSearchCriteria;
}>;


export type AreaTypesQuery = { areaTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AreaTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AreaTypeByTokenQuery = { areaTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateAreaTypeMutationVariables = Exact<{
  request?: AreaTypeCreateRequest | null | undefined;
}>;


export type CreateAreaTypeMutation = { createAreaType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateAreaTypeMutationVariables = Exact<{
  token: string;
  request?: AreaTypeCreateRequest | null | undefined;
}>;


export type UpdateAreaTypeMutation = { updateAreaType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteAreaTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAreaTypeMutation = { deleteAreaType: boolean };

export type AssetsQueryVariables = Exact<{
  criteria: AssetSearchCriteria;
}>;


export type AssetsQuery = { assets: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AssetByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AssetByTokenQuery = { assetsByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }> };

export type CreateAssetMutationVariables = Exact<{
  request?: AssetCreateRequest | null | undefined;
}>;


export type CreateAssetMutation = { createAsset: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type UpdateAssetMutationVariables = Exact<{
  token: string;
  request?: AssetCreateRequest | null | undefined;
}>;


export type UpdateAssetMutation = { updateAsset: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type DeleteAssetMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAssetMutation = { deleteAsset: boolean };

export type AssetTypesQueryVariables = Exact<{
  criteria: AssetTypeSearchCriteria;
}>;


export type AssetTypesQuery = { assetTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AssetTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AssetTypeByTokenQuery = { assetTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateAssetTypeMutationVariables = Exact<{
  request?: AssetTypeCreateRequest | null | undefined;
}>;


export type CreateAssetTypeMutation = { createAssetType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateAssetTypeMutationVariables = Exact<{
  token: string;
  request?: AssetTypeCreateRequest | null | undefined;
}>;


export type UpdateAssetTypeMutation = { updateAssetType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteAssetTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAssetTypeMutation = { deleteAssetType: boolean };

export type CustomersQueryVariables = Exact<{
  criteria: CustomerSearchCriteria;
}>;


export type CustomersQuery = { customers: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CustomerByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CustomerByTokenQuery = { customersByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }> };

export type CreateCustomerMutationVariables = Exact<{
  request?: CustomerCreateRequest | null | undefined;
}>;


export type CreateCustomerMutation = { createCustomer: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type UpdateCustomerMutationVariables = Exact<{
  token: string;
  request?: CustomerCreateRequest | null | undefined;
}>;


export type UpdateCustomerMutation = { updateCustomer: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } } };

export type DeleteCustomerMutationVariables = Exact<{
  token: string;
}>;


export type DeleteCustomerMutation = { deleteCustomer: boolean };

export type CustomerTypesQueryVariables = Exact<{
  criteria: CustomerTypeSearchCriteria;
}>;


export type CustomerTypesQuery = { customerTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CustomerTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CustomerTypeByTokenQuery = { customerTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateCustomerTypeMutationVariables = Exact<{
  request?: CustomerTypeCreateRequest | null | undefined;
}>;


export type CreateCustomerTypeMutation = { createCustomerType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateCustomerTypeMutationVariables = Exact<{
  token: string;
  request?: CustomerTypeCreateRequest | null | undefined;
}>;


export type UpdateCustomerTypeMutation = { updateCustomerType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteCustomerTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteCustomerTypeMutation = { deleteCustomerType: boolean };

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

export const AreasDocument = new TypedDocumentString(`
    query Areas($criteria: AreaSearchCriteria!) {
  areas(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      areaType {
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
    `) as unknown as TypedDocumentString<AreasQuery, AreasQueryVariables>;
export const AreaByTokenDocument = new TypedDocumentString(`
    query AreaByToken($tokens: [String!]!) {
  areasByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    areaType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<AreaByTokenQuery, AreaByTokenQueryVariables>;
export const CreateAreaDocument = new TypedDocumentString(`
    mutation CreateArea($request: AreaCreateRequest) {
  createArea(request: $request) {
    id
    token
    name
    description
    createdAt
    areaType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateAreaMutation, CreateAreaMutationVariables>;
export const UpdateAreaDocument = new TypedDocumentString(`
    mutation UpdateArea($token: String!, $request: AreaCreateRequest) {
  updateArea(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    areaType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateAreaMutation, UpdateAreaMutationVariables>;
export const DeleteAreaDocument = new TypedDocumentString(`
    mutation DeleteArea($token: String!) {
  deleteArea(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAreaMutation, DeleteAreaMutationVariables>;
export const AreaTypesDocument = new TypedDocumentString(`
    query AreaTypes($criteria: AreaTypeSearchCriteria!) {
  areaTypes(criteria: $criteria) {
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
    `) as unknown as TypedDocumentString<AreaTypesQuery, AreaTypesQueryVariables>;
export const AreaTypeByTokenDocument = new TypedDocumentString(`
    query AreaTypeByToken($tokens: [String!]!) {
  areaTypesByToken(tokens: $tokens) {
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
    `) as unknown as TypedDocumentString<AreaTypeByTokenQuery, AreaTypeByTokenQueryVariables>;
export const CreateAreaTypeDocument = new TypedDocumentString(`
    mutation CreateAreaType($request: AreaTypeCreateRequest) {
  createAreaType(request: $request) {
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
    `) as unknown as TypedDocumentString<CreateAreaTypeMutation, CreateAreaTypeMutationVariables>;
export const UpdateAreaTypeDocument = new TypedDocumentString(`
    mutation UpdateAreaType($token: String!, $request: AreaTypeCreateRequest) {
  updateAreaType(token: $token, request: $request) {
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
    `) as unknown as TypedDocumentString<UpdateAreaTypeMutation, UpdateAreaTypeMutationVariables>;
export const DeleteAreaTypeDocument = new TypedDocumentString(`
    mutation DeleteAreaType($token: String!) {
  deleteAreaType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAreaTypeMutation, DeleteAreaTypeMutationVariables>;
export const AssetsDocument = new TypedDocumentString(`
    query Assets($criteria: AssetSearchCriteria!) {
  assets(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      assetType {
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
    `) as unknown as TypedDocumentString<AssetsQuery, AssetsQueryVariables>;
export const AssetByTokenDocument = new TypedDocumentString(`
    query AssetByToken($tokens: [String!]!) {
  assetsByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    assetType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<AssetByTokenQuery, AssetByTokenQueryVariables>;
export const CreateAssetDocument = new TypedDocumentString(`
    mutation CreateAsset($request: AssetCreateRequest) {
  createAsset(request: $request) {
    id
    token
    name
    description
    createdAt
    assetType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateAssetMutation, CreateAssetMutationVariables>;
export const UpdateAssetDocument = new TypedDocumentString(`
    mutation UpdateAsset($token: String!, $request: AssetCreateRequest) {
  updateAsset(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    assetType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateAssetMutation, UpdateAssetMutationVariables>;
export const DeleteAssetDocument = new TypedDocumentString(`
    mutation DeleteAsset($token: String!) {
  deleteAsset(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAssetMutation, DeleteAssetMutationVariables>;
export const AssetTypesDocument = new TypedDocumentString(`
    query AssetTypes($criteria: AssetTypeSearchCriteria!) {
  assetTypes(criteria: $criteria) {
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
    `) as unknown as TypedDocumentString<AssetTypesQuery, AssetTypesQueryVariables>;
export const AssetTypeByTokenDocument = new TypedDocumentString(`
    query AssetTypeByToken($tokens: [String!]!) {
  assetTypesByToken(tokens: $tokens) {
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
    `) as unknown as TypedDocumentString<AssetTypeByTokenQuery, AssetTypeByTokenQueryVariables>;
export const CreateAssetTypeDocument = new TypedDocumentString(`
    mutation CreateAssetType($request: AssetTypeCreateRequest) {
  createAssetType(request: $request) {
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
    `) as unknown as TypedDocumentString<CreateAssetTypeMutation, CreateAssetTypeMutationVariables>;
export const UpdateAssetTypeDocument = new TypedDocumentString(`
    mutation UpdateAssetType($token: String!, $request: AssetTypeCreateRequest) {
  updateAssetType(token: $token, request: $request) {
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
    `) as unknown as TypedDocumentString<UpdateAssetTypeMutation, UpdateAssetTypeMutationVariables>;
export const DeleteAssetTypeDocument = new TypedDocumentString(`
    mutation DeleteAssetType($token: String!) {
  deleteAssetType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAssetTypeMutation, DeleteAssetTypeMutationVariables>;
export const CustomersDocument = new TypedDocumentString(`
    query Customers($criteria: CustomerSearchCriteria!) {
  customers(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      customerType {
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
    `) as unknown as TypedDocumentString<CustomersQuery, CustomersQueryVariables>;
export const CustomerByTokenDocument = new TypedDocumentString(`
    query CustomerByToken($tokens: [String!]!) {
  customersByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    customerType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<CustomerByTokenQuery, CustomerByTokenQueryVariables>;
export const CreateCustomerDocument = new TypedDocumentString(`
    mutation CreateCustomer($request: CustomerCreateRequest) {
  createCustomer(request: $request) {
    id
    token
    name
    description
    createdAt
    customerType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateCustomerMutation, CreateCustomerMutationVariables>;
export const UpdateCustomerDocument = new TypedDocumentString(`
    mutation UpdateCustomer($token: String!, $request: CustomerCreateRequest) {
  updateCustomer(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    customerType {
      id
      token
      name
      backgroundColor
      foregroundColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateCustomerMutation, UpdateCustomerMutationVariables>;
export const DeleteCustomerDocument = new TypedDocumentString(`
    mutation DeleteCustomer($token: String!) {
  deleteCustomer(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteCustomerMutation, DeleteCustomerMutationVariables>;
export const CustomerTypesDocument = new TypedDocumentString(`
    query CustomerTypes($criteria: CustomerTypeSearchCriteria!) {
  customerTypes(criteria: $criteria) {
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
    `) as unknown as TypedDocumentString<CustomerTypesQuery, CustomerTypesQueryVariables>;
export const CustomerTypeByTokenDocument = new TypedDocumentString(`
    query CustomerTypeByToken($tokens: [String!]!) {
  customerTypesByToken(tokens: $tokens) {
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
    `) as unknown as TypedDocumentString<CustomerTypeByTokenQuery, CustomerTypeByTokenQueryVariables>;
export const CreateCustomerTypeDocument = new TypedDocumentString(`
    mutation CreateCustomerType($request: CustomerTypeCreateRequest) {
  createCustomerType(request: $request) {
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
    `) as unknown as TypedDocumentString<CreateCustomerTypeMutation, CreateCustomerTypeMutationVariables>;
export const UpdateCustomerTypeDocument = new TypedDocumentString(`
    mutation UpdateCustomerType($token: String!, $request: CustomerTypeCreateRequest) {
  updateCustomerType(token: $token, request: $request) {
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
    `) as unknown as TypedDocumentString<UpdateCustomerTypeMutation, UpdateCustomerTypeMutationVariables>;
export const DeleteCustomerTypeDocument = new TypedDocumentString(`
    mutation DeleteCustomerType($token: String!) {
  deleteCustomerType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteCustomerTypeMutation, DeleteCustomerTypeMutationVariables>;
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