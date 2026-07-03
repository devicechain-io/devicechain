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
    "\n  query Settings {\n    settings {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n": typeof types.SettingsDocument,
    "\n  query TokenMasks {\n    tokenMasks\n  }\n": typeof types.TokenMasksDocument,
    "\n  mutation SetSetting($key: String!, $value: String!) {\n    setSetting(key: $key, value: $value) {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n": typeof types.SetSettingDocument,
    "\n  mutation ClearSetting($key: String!) {\n    clearSetting(key: $key) {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n": typeof types.ClearSettingDocument,
};
const documents: Documents = {
    "\n  query Settings {\n    settings {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n": types.SettingsDocument,
    "\n  query TokenMasks {\n    tokenMasks\n  }\n": types.TokenMasksDocument,
    "\n  mutation SetSetting($key: String!, $value: String!) {\n    setSetting(key: $key, value: $value) {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n": types.SetSettingDocument,
    "\n  mutation ClearSetting($key: String!) {\n    clearSetting(key: $key) {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n": types.ClearSettingDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Settings {\n    settings {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n"): typeof import('./graphql').SettingsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TokenMasks {\n    tokenMasks\n  }\n"): typeof import('./graphql').TokenMasksDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetSetting($key: String!, $value: String!) {\n    setSetting(key: $key, value: $value) {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n"): typeof import('./graphql').SetSettingDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ClearSetting($key: String!) {\n    clearSetting(key: $key) {\n      key\n      description\n      value\n      overridden\n      updatedAt\n      updatedBy\n    }\n  }\n"): typeof import('./graphql').ClearSettingDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
