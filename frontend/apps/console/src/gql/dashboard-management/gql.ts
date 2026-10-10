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
    "\n  query Dashboards($criteria: DashboardSearchCriteria!) {\n    dashboards(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        createdAt\n        updatedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DashboardsDocument,
    "\n  query Dashboard($token: String!) {\n    dashboard(token: $token) {\n      token\n      name\n      description\n      definition\n      updatedAt\n      publishedVersion\n    }\n  }\n": typeof types.DashboardDocument,
    "\n  query DashboardPublishedVersion($token: String!) {\n    dashboard(token: $token) {\n      publishedVersion\n    }\n  }\n": typeof types.DashboardPublishedVersionDocument,
    "\n  query PublishedDashboard($token: String!) {\n    publishedDashboard(token: $token) {\n      token\n      name\n      description\n      version\n      publishedAt\n      definition\n    }\n  }\n": typeof types.PublishedDashboardDocument,
    "\n  mutation CreateDashboard($request: DashboardCreateRequest!) {\n    createDashboard(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateDashboardDocument,
    "\n  mutation UpdateDashboard(\n    $token: String!\n    $request: DashboardUpdateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateDashboard(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": typeof types.UpdateDashboardDocument,
    "\n  query DashboardVersions($token: String!, $limit: Int) {\n    dashboardVersions(token: $token, limit: $limit) {\n      version\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n": typeof types.DashboardVersionsDocument,
    "\n  mutation PublishDashboard(\n    $token: String!\n    $label: String\n    $description: String\n    $expectedUpdatedAt: String\n  ) {\n    publishDashboard(\n      token: $token\n      label: $label\n      description: $description\n      expectedUpdatedAt: $expectedUpdatedAt\n    ) {\n      version {\n        version\n      }\n      dashboard {\n        updatedAt\n      }\n    }\n  }\n": typeof types.PublishDashboardDocument,
    "\n  mutation ActivateDashboardVersion($token: String!, $version: Int!) {\n    activateDashboardVersion(token: $token, version: $version) {\n      publishedVersion\n    }\n  }\n": typeof types.ActivateDashboardVersionDocument,
    "\n  mutation RollbackDashboard($token: String!, $version: Int!, $expectedUpdatedAt: String) {\n    rollbackDashboard(token: $token, version: $version, expectedUpdatedAt: $expectedUpdatedAt) {\n      definition\n      updatedAt\n    }\n  }\n": typeof types.RollbackDashboardDocument,
    "\n  mutation DeleteDashboard($token: String!) {\n    deleteDashboard(token: $token)\n  }\n": typeof types.DeleteDashboardDocument,
};
const documents: Documents = {
    "\n  query Dashboards($criteria: DashboardSearchCriteria!) {\n    dashboards(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        createdAt\n        updatedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DashboardsDocument,
    "\n  query Dashboard($token: String!) {\n    dashboard(token: $token) {\n      token\n      name\n      description\n      definition\n      updatedAt\n      publishedVersion\n    }\n  }\n": types.DashboardDocument,
    "\n  query DashboardPublishedVersion($token: String!) {\n    dashboard(token: $token) {\n      publishedVersion\n    }\n  }\n": types.DashboardPublishedVersionDocument,
    "\n  query PublishedDashboard($token: String!) {\n    publishedDashboard(token: $token) {\n      token\n      name\n      description\n      version\n      publishedAt\n      definition\n    }\n  }\n": types.PublishedDashboardDocument,
    "\n  mutation CreateDashboard($request: DashboardCreateRequest!) {\n    createDashboard(request: $request) {\n      token\n    }\n  }\n": types.CreateDashboardDocument,
    "\n  mutation UpdateDashboard(\n    $token: String!\n    $request: DashboardUpdateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateDashboard(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": types.UpdateDashboardDocument,
    "\n  query DashboardVersions($token: String!, $limit: Int) {\n    dashboardVersions(token: $token, limit: $limit) {\n      version\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n": types.DashboardVersionsDocument,
    "\n  mutation PublishDashboard(\n    $token: String!\n    $label: String\n    $description: String\n    $expectedUpdatedAt: String\n  ) {\n    publishDashboard(\n      token: $token\n      label: $label\n      description: $description\n      expectedUpdatedAt: $expectedUpdatedAt\n    ) {\n      version {\n        version\n      }\n      dashboard {\n        updatedAt\n      }\n    }\n  }\n": types.PublishDashboardDocument,
    "\n  mutation ActivateDashboardVersion($token: String!, $version: Int!) {\n    activateDashboardVersion(token: $token, version: $version) {\n      publishedVersion\n    }\n  }\n": types.ActivateDashboardVersionDocument,
    "\n  mutation RollbackDashboard($token: String!, $version: Int!, $expectedUpdatedAt: String) {\n    rollbackDashboard(token: $token, version: $version, expectedUpdatedAt: $expectedUpdatedAt) {\n      definition\n      updatedAt\n    }\n  }\n": types.RollbackDashboardDocument,
    "\n  mutation DeleteDashboard($token: String!) {\n    deleteDashboard(token: $token)\n  }\n": types.DeleteDashboardDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Dashboards($criteria: DashboardSearchCriteria!) {\n    dashboards(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        createdAt\n        updatedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DashboardsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Dashboard($token: String!) {\n    dashboard(token: $token) {\n      token\n      name\n      description\n      definition\n      updatedAt\n      publishedVersion\n    }\n  }\n"): typeof import('./graphql').DashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DashboardPublishedVersion($token: String!) {\n    dashboard(token: $token) {\n      publishedVersion\n    }\n  }\n"): typeof import('./graphql').DashboardPublishedVersionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query PublishedDashboard($token: String!) {\n    publishedDashboard(token: $token) {\n      token\n      name\n      description\n      version\n      publishedAt\n      definition\n    }\n  }\n"): typeof import('./graphql').PublishedDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDashboard($request: DashboardCreateRequest!) {\n    createDashboard(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDashboard(\n    $token: String!\n    $request: DashboardUpdateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateDashboard(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DashboardVersions($token: String!, $limit: Int) {\n    dashboardVersions(token: $token, limit: $limit) {\n      version\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n"): typeof import('./graphql').DashboardVersionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation PublishDashboard(\n    $token: String!\n    $label: String\n    $description: String\n    $expectedUpdatedAt: String\n  ) {\n    publishDashboard(\n      token: $token\n      label: $label\n      description: $description\n      expectedUpdatedAt: $expectedUpdatedAt\n    ) {\n      version {\n        version\n      }\n      dashboard {\n        updatedAt\n      }\n    }\n  }\n"): typeof import('./graphql').PublishDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ActivateDashboardVersion($token: String!, $version: Int!) {\n    activateDashboardVersion(token: $token, version: $version) {\n      publishedVersion\n    }\n  }\n"): typeof import('./graphql').ActivateDashboardVersionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RollbackDashboard($token: String!, $version: Int!, $expectedUpdatedAt: String) {\n    rollbackDashboard(token: $token, version: $version, expectedUpdatedAt: $expectedUpdatedAt) {\n      definition\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').RollbackDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDashboard($token: String!) {\n    deleteDashboard(token: $token)\n  }\n"): typeof import('./graphql').DeleteDashboardDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
