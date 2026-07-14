/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type ConnectorCreateRequest = {
  config: string;
  description?: string | null | undefined;
  name?: string | null | undefined;
  secret?: string | null | undefined;
  token: string;
  type: string;
};

export type ConnectorSearchCriteria = {
  pageNumber: number;
  pageSize: number;
  type?: string | null | undefined;
};

export type ConnectorsQueryVariables = Exact<{
  criteria: ConnectorSearchCriteria;
}>;


export type ConnectorsQuery = { connectors: { results: Array<{ token: string, name: string | null, description: string | null, type: string }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type ConnectorQueryVariables = Exact<{
  token: string;
}>;


export type ConnectorQuery = { connector: { id: string, token: string, name: string | null, description: string | null, type: string, config: string, hasSecret: boolean, updatedAt: string | null } | null };

export type ConnectorTypesQueryVariables = Exact<{ [key: string]: never; }>;


export type ConnectorTypesQuery = { connectorTypes: Array<string> };

export type CreateConnectorMutationVariables = Exact<{
  request: ConnectorCreateRequest;
}>;


export type CreateConnectorMutation = { createConnector: { token: string } };

export type UpdateConnectorMutationVariables = Exact<{
  token: string;
  request: ConnectorCreateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateConnectorMutation = { updateConnector: { token: string, updatedAt: string | null } };

export type ConnectorVersionsQueryVariables = Exact<{
  token: string;
}>;


export type ConnectorVersionsQuery = { connectorVersions: Array<{ version: number, type: string, label: string | null, description: string | null, publishedAt: string, publishedBy: string | null }> };

export type PublishConnectorMutationVariables = Exact<{
  token: string;
  label?: string | null | undefined;
  description?: string | null | undefined;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type PublishConnectorMutation = { publishConnector: { version: number } };

export type RollbackConnectorMutationVariables = Exact<{
  token: string;
  version: number;
}>;


export type RollbackConnectorMutation = { rollbackConnector: { type: string, config: string, updatedAt: string | null } };

export type DeleteConnectorMutationVariables = Exact<{
  token: string;
}>;


export type DeleteConnectorMutation = { deleteConnector: boolean };

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

export const ConnectorsDocument = new TypedDocumentString(`
    query Connectors($criteria: ConnectorSearchCriteria!) {
  connectors(criteria: $criteria) {
    results {
      token
      name
      description
      type
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<ConnectorsQuery, ConnectorsQueryVariables>;
export const ConnectorDocument = new TypedDocumentString(`
    query Connector($token: String!) {
  connector(token: $token) {
    id
    token
    name
    description
    type
    config
    hasSecret
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<ConnectorQuery, ConnectorQueryVariables>;
export const ConnectorTypesDocument = new TypedDocumentString(`
    query ConnectorTypes {
  connectorTypes
}
    `) as unknown as TypedDocumentString<ConnectorTypesQuery, ConnectorTypesQueryVariables>;
export const CreateConnectorDocument = new TypedDocumentString(`
    mutation CreateConnector($request: ConnectorCreateRequest!) {
  createConnector(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateConnectorMutation, CreateConnectorMutationVariables>;
export const UpdateConnectorDocument = new TypedDocumentString(`
    mutation UpdateConnector($token: String!, $request: ConnectorCreateRequest!, $expectedUpdatedAt: String) {
  updateConnector(
    token: $token
    request: $request
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateConnectorMutation, UpdateConnectorMutationVariables>;
export const ConnectorVersionsDocument = new TypedDocumentString(`
    query ConnectorVersions($token: String!) {
  connectorVersions(token: $token) {
    version
    type
    label
    description
    publishedAt
    publishedBy
  }
}
    `) as unknown as TypedDocumentString<ConnectorVersionsQuery, ConnectorVersionsQueryVariables>;
export const PublishConnectorDocument = new TypedDocumentString(`
    mutation PublishConnector($token: String!, $label: String, $description: String, $expectedUpdatedAt: String) {
  publishConnector(
    token: $token
    label: $label
    description: $description
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    version
  }
}
    `) as unknown as TypedDocumentString<PublishConnectorMutation, PublishConnectorMutationVariables>;
export const RollbackConnectorDocument = new TypedDocumentString(`
    mutation RollbackConnector($token: String!, $version: Int!) {
  rollbackConnector(token: $token, version: $version) {
    type
    config
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<RollbackConnectorMutation, RollbackConnectorMutationVariables>;
export const DeleteConnectorDocument = new TypedDocumentString(`
    mutation DeleteConnector($token: String!) {
  deleteConnector(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteConnectorMutation, DeleteConnectorMutationVariables>;