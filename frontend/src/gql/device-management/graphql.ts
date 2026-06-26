/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DeviceSearchCriteria = {
  deviceType?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type DeviceTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type DevicesQueryVariables = Exact<{
  criteria: DeviceSearchCriteria;
}>;


export type DevicesQuery = { devices: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, backgroundColor: string | null, foregroundColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceTypesQueryVariables = Exact<{
  criteria: DeviceTypeSearchCriteria;
}>;


export type DeviceTypesQuery = { deviceTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

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