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
    "\n  query Connectors($criteria: ConnectorSearchCriteria!) {\n    connectors(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        type\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.ConnectorsDocument,
    "\n  query Connector($token: String!) {\n    connector(token: $token) {\n      id\n      token\n      name\n      description\n      type\n      config\n      hasSecret\n      updatedAt\n    }\n  }\n": typeof types.ConnectorDocument,
    "\n  query ConnectorTypes {\n    connectorTypes\n  }\n": typeof types.ConnectorTypesDocument,
    "\n  mutation CreateConnector($request: ConnectorCreateRequest!) {\n    createConnector(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateConnectorDocument,
    "\n  mutation UpdateConnector(\n    $token: String!\n    $request: ConnectorCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateConnector(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": typeof types.UpdateConnectorDocument,
    "\n  query ConnectorVersions($token: String!) {\n    connectorVersions(token: $token) {\n      version\n      type\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n": typeof types.ConnectorVersionsDocument,
    "\n  mutation PublishConnector(\n    $token: String!\n    $label: String\n    $description: String\n    $expectedUpdatedAt: String\n  ) {\n    publishConnector(\n      token: $token\n      label: $label\n      description: $description\n      expectedUpdatedAt: $expectedUpdatedAt\n    ) {\n      version\n    }\n  }\n": typeof types.PublishConnectorDocument,
    "\n  mutation RollbackConnector($token: String!, $version: Int!) {\n    rollbackConnector(token: $token, version: $version) {\n      type\n      config\n      updatedAt\n    }\n  }\n": typeof types.RollbackConnectorDocument,
    "\n  mutation DeleteConnector($token: String!) {\n    deleteConnector(token: $token)\n  }\n": typeof types.DeleteConnectorDocument,
};
const documents: Documents = {
    "\n  query Connectors($criteria: ConnectorSearchCriteria!) {\n    connectors(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        type\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.ConnectorsDocument,
    "\n  query Connector($token: String!) {\n    connector(token: $token) {\n      id\n      token\n      name\n      description\n      type\n      config\n      hasSecret\n      updatedAt\n    }\n  }\n": types.ConnectorDocument,
    "\n  query ConnectorTypes {\n    connectorTypes\n  }\n": types.ConnectorTypesDocument,
    "\n  mutation CreateConnector($request: ConnectorCreateRequest!) {\n    createConnector(request: $request) {\n      token\n    }\n  }\n": types.CreateConnectorDocument,
    "\n  mutation UpdateConnector(\n    $token: String!\n    $request: ConnectorCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateConnector(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": types.UpdateConnectorDocument,
    "\n  query ConnectorVersions($token: String!) {\n    connectorVersions(token: $token) {\n      version\n      type\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n": types.ConnectorVersionsDocument,
    "\n  mutation PublishConnector(\n    $token: String!\n    $label: String\n    $description: String\n    $expectedUpdatedAt: String\n  ) {\n    publishConnector(\n      token: $token\n      label: $label\n      description: $description\n      expectedUpdatedAt: $expectedUpdatedAt\n    ) {\n      version\n    }\n  }\n": types.PublishConnectorDocument,
    "\n  mutation RollbackConnector($token: String!, $version: Int!) {\n    rollbackConnector(token: $token, version: $version) {\n      type\n      config\n      updatedAt\n    }\n  }\n": types.RollbackConnectorDocument,
    "\n  mutation DeleteConnector($token: String!) {\n    deleteConnector(token: $token)\n  }\n": types.DeleteConnectorDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Connectors($criteria: ConnectorSearchCriteria!) {\n    connectors(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        type\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').ConnectorsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Connector($token: String!) {\n    connector(token: $token) {\n      id\n      token\n      name\n      description\n      type\n      config\n      hasSecret\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').ConnectorDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query ConnectorTypes {\n    connectorTypes\n  }\n"): typeof import('./graphql').ConnectorTypesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateConnector($request: ConnectorCreateRequest!) {\n    createConnector(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateConnectorDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateConnector(\n    $token: String!\n    $request: ConnectorCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateConnector(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateConnectorDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query ConnectorVersions($token: String!) {\n    connectorVersions(token: $token) {\n      version\n      type\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n"): typeof import('./graphql').ConnectorVersionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation PublishConnector(\n    $token: String!\n    $label: String\n    $description: String\n    $expectedUpdatedAt: String\n  ) {\n    publishConnector(\n      token: $token\n      label: $label\n      description: $description\n      expectedUpdatedAt: $expectedUpdatedAt\n    ) {\n      version\n    }\n  }\n"): typeof import('./graphql').PublishConnectorDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RollbackConnector($token: String!, $version: Int!) {\n    rollbackConnector(token: $token, version: $version) {\n      type\n      config\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').RollbackConnectorDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteConnector($token: String!) {\n    deleteConnector(token: $token)\n  }\n"): typeof import('./graphql').DeleteConnectorDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
