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
    "\n  query Identities {\n    identities {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.IdentitiesDocument,
    "\n  query Tenants {\n    tenants {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.TenantsDocument,
    "\n  query Roles($scope: String) {\n    roles(scope: $scope) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.RolesDocument,
    "\n  query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        tenant\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AdminAuditEventsDocument,
    "\n  query Authorities {\n    authorities\n  }\n": typeof types.AuthoritiesDocument,
    "\n  mutation CreateIdentity($request: AdminIdentityCreateRequest!) {\n    createIdentity(request: $request) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateIdentityDocument,
    "\n  mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {\n    setIdentityEnabled(email: $email, enabled: $enabled) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.SetIdentityEnabledDocument,
    "\n  mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {\n    setSystemRoles(email: $email, roleTokens: $roleTokens) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.SetSystemRolesDocument,
    "\n  mutation SetPassword($email: String!, $password: String!) {\n    setPassword(email: $email, password: $password) {\n      id\n      email\n      enabled\n    }\n  }\n": typeof types.SetPasswordDocument,
    "\n  mutation DeleteIdentity($email: String!) {\n    deleteIdentity(email: $email)\n  }\n": typeof types.DeleteIdentityDocument,
    "\n  mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.AddMembershipDocument,
    "\n  mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.SetMembershipRolesDocument,
    "\n  mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {\n    setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.SetMembershipEnabledDocument,
    "\n  mutation RemoveMembership($email: String!, $tenant: String!) {\n    removeMembership(email: $email, tenant: $tenant) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.RemoveMembershipDocument,
    "\n  mutation CreateRole($request: AdminRoleCreateRequest!) {\n    createRole(request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateRoleDocument,
    "\n  mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {\n    updateRole(scope: $scope, token: $token, request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.UpdateRoleDocument,
    "\n  mutation DeleteRole($scope: String!, $token: String!) {\n    deleteRole(scope: $scope, token: $token)\n  }\n": typeof types.DeleteRoleDocument,
    "\n  mutation CreateTenant($request: AdminTenantCreateRequest!) {\n    createTenant(request: $request) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateTenantDocument,
    "\n  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {\n    updateTenant(token: $token, request: $request) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.UpdateTenantDocument,
    "\n  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {\n    setTenantEnabled(token: $token, enabled: $enabled) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.SetTenantEnabledDocument,
    "\n  mutation DeleteTenant($token: String!) {\n    deleteTenant(token: $token)\n  }\n": typeof types.DeleteTenantDocument,
};
const documents: Documents = {
    "\n  query Identities {\n    identities {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.IdentitiesDocument,
    "\n  query Tenants {\n    tenants {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": types.TenantsDocument,
    "\n  query Roles($scope: String) {\n    roles(scope: $scope) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.RolesDocument,
    "\n  query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        tenant\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AdminAuditEventsDocument,
    "\n  query Authorities {\n    authorities\n  }\n": types.AuthoritiesDocument,
    "\n  mutation CreateIdentity($request: AdminIdentityCreateRequest!) {\n    createIdentity(request: $request) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateIdentityDocument,
    "\n  mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {\n    setIdentityEnabled(email: $email, enabled: $enabled) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.SetIdentityEnabledDocument,
    "\n  mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {\n    setSystemRoles(email: $email, roleTokens: $roleTokens) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.SetSystemRolesDocument,
    "\n  mutation SetPassword($email: String!, $password: String!) {\n    setPassword(email: $email, password: $password) {\n      id\n      email\n      enabled\n    }\n  }\n": types.SetPasswordDocument,
    "\n  mutation DeleteIdentity($email: String!) {\n    deleteIdentity(email: $email)\n  }\n": types.DeleteIdentityDocument,
    "\n  mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.AddMembershipDocument,
    "\n  mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.SetMembershipRolesDocument,
    "\n  mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {\n    setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.SetMembershipEnabledDocument,
    "\n  mutation RemoveMembership($email: String!, $tenant: String!) {\n    removeMembership(email: $email, tenant: $tenant) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.RemoveMembershipDocument,
    "\n  mutation CreateRole($request: AdminRoleCreateRequest!) {\n    createRole(request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateRoleDocument,
    "\n  mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {\n    updateRole(scope: $scope, token: $token, request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.UpdateRoleDocument,
    "\n  mutation DeleteRole($scope: String!, $token: String!) {\n    deleteRole(scope: $scope, token: $token)\n  }\n": types.DeleteRoleDocument,
    "\n  mutation CreateTenant($request: AdminTenantCreateRequest!) {\n    createTenant(request: $request) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateTenantDocument,
    "\n  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {\n    updateTenant(token: $token, request: $request) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": types.UpdateTenantDocument,
    "\n  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {\n    setTenantEnabled(token: $token, enabled: $enabled) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n": types.SetTenantEnabledDocument,
    "\n  mutation DeleteTenant($token: String!) {\n    deleteTenant(token: $token)\n  }\n": types.DeleteTenantDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Identities {\n    identities {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').IdentitiesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Tenants {\n    tenants {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').TenantsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Roles($scope: String) {\n    roles(scope: $scope) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').RolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        tenant\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AdminAuditEventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Authorities {\n    authorities\n  }\n"): typeof import('./graphql').AuthoritiesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateIdentity($request: AdminIdentityCreateRequest!) {\n    createIdentity(request: $request) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateIdentityDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {\n    setIdentityEnabled(email: $email, enabled: $enabled) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').SetIdentityEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {\n    setSystemRoles(email: $email, roleTokens: $roleTokens) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').SetSystemRolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetPassword($email: String!, $password: String!) {\n    setPassword(email: $email, password: $password) {\n      id\n      email\n      enabled\n    }\n  }\n"): typeof import('./graphql').SetPasswordDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteIdentity($email: String!) {\n    deleteIdentity(email: $email)\n  }\n"): typeof import('./graphql').DeleteIdentityDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').AddMembershipDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').SetMembershipRolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {\n    setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').SetMembershipEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RemoveMembership($email: String!, $tenant: String!) {\n    removeMembership(email: $email, tenant: $tenant) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').RemoveMembershipDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateRole($request: AdminRoleCreateRequest!) {\n    createRole(request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {\n    updateRole(scope: $scope, token: $token, request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteRole($scope: String!, $token: String!) {\n    deleteRole(scope: $scope, token: $token)\n  }\n"): typeof import('./graphql').DeleteRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateTenant($request: AdminTenantCreateRequest!) {\n    createTenant(request: $request) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {\n    updateTenant(token: $token, request: $request) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {\n    setTenantEnabled(token: $token, enabled: $enabled) {\n      id\n      token\n      name\n      enabled\n      config\n      ingestMessagesPerSecond\n      ingestBurst\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').SetTenantEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteTenant($token: String!) {\n    deleteTenant(token: $token)\n  }\n"): typeof import('./graphql').DeleteTenantDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
