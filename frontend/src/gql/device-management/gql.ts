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
    "\n  query Devices($criteria: DeviceSearchCriteria!) {\n    devices(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        deviceType {\n          id\n          token\n          name\n          backgroundColor\n          foregroundColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DevicesDocument,
    "\n  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {\n    deviceTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DeviceTypesDocument,
};
const documents: Documents = {
    "\n  query Devices($criteria: DeviceSearchCriteria!) {\n    devices(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        deviceType {\n          id\n          token\n          name\n          backgroundColor\n          foregroundColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DevicesDocument,
    "\n  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {\n    deviceTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DeviceTypesDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Devices($criteria: DeviceSearchCriteria!) {\n    devices(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        deviceType {\n          id\n          token\n          name\n          backgroundColor\n          foregroundColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DevicesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {\n    deviceTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceTypesDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
