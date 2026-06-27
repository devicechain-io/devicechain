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
    "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n      }\n    }\n  }\n": typeof types.LoginDocument,
    "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": typeof types.SelectTenantDocument,
    "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": typeof types.RefreshDocument,
    "\n  query Users {\n    users {\n      id\n      username\n      email\n      firstName\n      lastName\n      enabled\n      createdAt\n      updatedAt\n      roles {\n        id\n        token\n        name\n        description\n        authorities\n        createdAt\n        updatedAt\n      }\n    }\n  }\n": typeof types.UsersDocument,
    "\n  query Roles {\n    roles {\n      id\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.RolesDocument,
};
const documents: Documents = {
    "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n      }\n    }\n  }\n": types.LoginDocument,
    "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": types.SelectTenantDocument,
    "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": types.RefreshDocument,
    "\n  query Users {\n    users {\n      id\n      username\n      email\n      firstName\n      lastName\n      enabled\n      createdAt\n      updatedAt\n      roles {\n        id\n        token\n        name\n        description\n        authorities\n        createdAt\n        updatedAt\n      }\n    }\n  }\n": types.UsersDocument,
    "\n  query Roles {\n    roles {\n      id\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.RolesDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').LoginDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n"): typeof import('./graphql').SelectTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n"): typeof import('./graphql').RefreshDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Users {\n    users {\n      id\n      username\n      email\n      firstName\n      lastName\n      enabled\n      createdAt\n      updatedAt\n      roles {\n        id\n        token\n        name\n        description\n        authorities\n        createdAt\n        updatedAt\n      }\n    }\n  }\n"): typeof import('./graphql').UsersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Roles {\n    roles {\n      id\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').RolesDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
