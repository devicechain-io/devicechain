/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/**
 * Fields for a new connector. The type must be one this deployment can deliver to, and
 * the config must be valid for that type; either failing rejects the create.
 */
export type ConnectorCreateRequest = {
  /**
   * The connection settings for the type, as a JSON object serialized to a string of at
   * most 64 KiB. Unknown keys are rejected. Keys by type: mqtt takes urls (required, one
   * broker per entry), topic (required), qos (0, 1 or 2; default 1), clientId and
   * username; kafka takes addresses (required, host:port each), topic (required),
   * clientId, tls and sasl ({mechanism: PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512, username});
   * aws_sns takes region, accessKeyId and topicArn (all required) and endpoint; aws_sqs
   * takes region, accessKeyId and url (all required) and endpoint. mqtt urls
   * (scheme://host:port) and kafka addresses must give an explicit port; the AWS endpoint
   * and the SQS url are http or https URLs. The platform restricts which addresses a
   * connector may reach.
   */
  config: string;
  /** Free-text description of what the connector delivers to. */
  description?: string | null | undefined;
  /** Human-readable name shown in connector lists. */
  name?: string | null | undefined;
  /**
   * The credential: the broker password for mqtt, the SASL password for kafka, the secret
   * access key for aws_sns and aws_sqs. Write-only; stored encrypted and never returned.
   * Omit it, or send an empty string, for no credential. aws_sns and aws_sqs require one
   * at delivery time, as does kafka when sasl is set.
   */
  secret?: string | null | undefined;
  /**
   * Unique identifier for the new connector within the tenant. Letters, digits, hyphens
   * and underscores, starting with a letter or digit, at most 128 characters.
   */
  token: string;
  /**
   * The connector type; must be one of the values returned by connectorTypes that this
   * deployment can deliver to. A recognized type with no delivery client in this build is
   * refused with extensions.code UNSUPPORTED.
   */
  type: string;
};

/** Filter and paging for the connectors query. */
export type ConnectorSearchCriteria = {
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Connectors per page. A value below 1 is treated as 100; a value above 1000 is capped at 1000. */
  pageSize: number;
  /** Return only connectors of this type. Omit it to return every type. */
  type?: string | null | undefined;
};

/**
 * A partial update to a connector's draft. Omit a field to leave the stored value alone,
 * send a value to set it, or send an explicit null to clear it (except where noted). The
 * connector is named by the mutation's token argument, so there is no token here; use
 * renameConnector to change the token.
 */
export type ConnectorUpdateRequest = {
  /**
   * New connection settings, as a JSON object serialized to a string of at most 64 KiB.
   * Omit it to keep the stored config; an explicit null is refused. It is validated
   * against the connector's type after this update is applied.
   */
  config?: string | null | undefined;
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
  /**
   * The write-only credential. Omit it to keep the stored credential, send a value to
   * replace it, or send null or an empty string to delete it.
   */
  secret?: string | null | undefined;
  /**
   * New connector type. Omit it to keep the stored type; an explicit null is refused. The
   * stored config is checked against the new type, so a change that leaves the config
   * invalid for it is rejected.
   */
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
  request: ConnectorUpdateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateConnectorMutation = { updateConnector: { token: string, updatedAt: string | null } };

export type RenameConnectorMutationVariables = Exact<{
  token: string;
  newToken: string;
}>;


export type RenameConnectorMutation = { renameConnector: { token: string, updatedAt: string | null } };

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
    mutation UpdateConnector($token: String!, $request: ConnectorUpdateRequest!, $expectedUpdatedAt: String) {
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
export const RenameConnectorDocument = new TypedDocumentString(`
    mutation RenameConnector($token: String!, $newToken: String!) {
  renameConnector(token: $token, newToken: $newToken) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<RenameConnectorMutation, RenameConnectorMutationVariables>;
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