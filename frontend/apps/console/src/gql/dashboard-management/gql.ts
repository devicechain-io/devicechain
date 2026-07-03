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
    "\n  query Dashboard($token: String!) {\n    dashboard(token: $token) {\n      token\n      name\n      description\n      definition\n    }\n  }\n": typeof types.DashboardDocument,
    "\n  mutation CreateDashboard($request: DashboardCreateRequest!) {\n    createDashboard(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateDashboardDocument,
    "\n  mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!) {\n    updateDashboard(token: $token, request: $request) {\n      token\n    }\n  }\n": typeof types.UpdateDashboardDocument,
    "\n  mutation DeleteDashboard($token: String!) {\n    deleteDashboard(token: $token)\n  }\n": typeof types.DeleteDashboardDocument,
};
const documents: Documents = {
    "\n  query Dashboards($criteria: DashboardSearchCriteria!) {\n    dashboards(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        createdAt\n        updatedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DashboardsDocument,
    "\n  query Dashboard($token: String!) {\n    dashboard(token: $token) {\n      token\n      name\n      description\n      definition\n    }\n  }\n": types.DashboardDocument,
    "\n  mutation CreateDashboard($request: DashboardCreateRequest!) {\n    createDashboard(request: $request) {\n      token\n    }\n  }\n": types.CreateDashboardDocument,
    "\n  mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!) {\n    updateDashboard(token: $token, request: $request) {\n      token\n    }\n  }\n": types.UpdateDashboardDocument,
    "\n  mutation DeleteDashboard($token: String!) {\n    deleteDashboard(token: $token)\n  }\n": types.DeleteDashboardDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Dashboards($criteria: DashboardSearchCriteria!) {\n    dashboards(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        createdAt\n        updatedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DashboardsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Dashboard($token: String!) {\n    dashboard(token: $token) {\n      token\n      name\n      description\n      definition\n    }\n  }\n"): typeof import('./graphql').DashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDashboard($request: DashboardCreateRequest!) {\n    createDashboard(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!) {\n    updateDashboard(token: $token, request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').UpdateDashboardDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDashboard($token: String!) {\n    deleteDashboard(token: $token)\n  }\n"): typeof import('./graphql').DeleteDashboardDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
