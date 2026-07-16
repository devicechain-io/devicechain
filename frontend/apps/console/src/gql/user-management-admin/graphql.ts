/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type AdminAuditEventSearchCriteria = {
  actor?: string | null | undefined;
  category?: string | null | undefined;
  endTime?: string | null | undefined;
  operation?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
  startTime?: string | null | undefined;
  tenant?: string | null | undefined;
};

export type AdminIdentityCreateRequest = {
  email: string;
  enabled: boolean;
  firstName?: string | null | undefined;
  lastName?: string | null | undefined;
  password: string;
  systemRoles: Array<string>;
};

export type AdminRoleCreateRequest = {
  authorities: Array<string>;
  description?: string | null | undefined;
  name?: string | null | undefined;
  scope: string;
  token: string;
};

export type AdminRoleUpdateRequest = {
  authorities: Array<string>;
  description?: string | null | undefined;
  name?: string | null | undefined;
};

export type AdminTenantCreateRequest = {
  aiExternalEnabled?: boolean | null | undefined;
  aiInferenceBurst?: number | null | undefined;
  aiInferenceRequestsPerMinute?: number | null | undefined;
  config?: string | null | undefined;
  ingestBurst?: number | null | undefined;
  ingestMessagesPerSecond?: number | null | undefined;
  name?: string | null | undefined;
  outboundBurst?: number | null | undefined;
  outboundMessagesPerSecond?: number | null | undefined;
  tierToken: string;
  token: string;
};

export type AdminTenantTierCreateRequest = {
  config?: string | null | undefined;
  description?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AdminTenantTierUpdateRequest = {
  config?: string | null | undefined;
  description?: string | null | undefined;
  name?: string | null | undefined;
};

export type AdminTenantUpdateRequest = {
  aiExternalEnabled?: boolean | null | undefined;
  aiInferenceBurst?: number | null | undefined;
  aiInferenceRequestsPerMinute?: number | null | undefined;
  config?: string | null | undefined;
  ingestBurst?: number | null | undefined;
  ingestMessagesPerSecond?: number | null | undefined;
  name?: string | null | undefined;
  outboundBurst?: number | null | undefined;
  outboundMessagesPerSecond?: number | null | undefined;
  tierToken: string;
};

export type IdentitiesQueryVariables = Exact<{ [key: string]: never; }>;


export type IdentitiesQuery = { identities: Array<{ id: string, email: string, firstName: string | null, lastName: string | null, enabled: boolean, systemRoles: Array<string>, createdAt: string | null, updatedAt: string | null, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> }> };

export type TenantsQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantsQuery = { tenants: Array<{ id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null }, effectiveSettings: Array<{ dimension: { name: string, label: string, rateUnit: string }, rate: { source: string, value: number | null, tier: number | null, override: number | null }, burst: { source: string, value: number | null, tier: number | null, override: number | null } }> }> };

export type GovernanceDimensionsQueryVariables = Exact<{ [key: string]: never; }>;


export type GovernanceDimensionsQuery = { governanceDimensions: Array<{ name: string, label: string, rateField: string, burstField: string, rateUnit: string }> };

export type TenantTiersQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantTiersQuery = { tenantTiers: Array<{ id: string, token: string, name: string | null, description: string | null }> };

export type TenantTierCatalogQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantTierCatalogQuery = { tenantTiers: Array<{ id: string, token: string, name: string | null, description: string | null, config: string | null, tenantCount: number, createdAt: string | null, updatedAt: string | null }> };

export type CreateTenantTierMutationVariables = Exact<{
  request: AdminTenantTierCreateRequest;
}>;


export type CreateTenantTierMutation = { createTenantTier: { id: string, token: string, name: string | null, description: string | null, config: string | null, tenantCount: number, createdAt: string | null, updatedAt: string | null } };

export type UpdateTenantTierMutationVariables = Exact<{
  token: string;
  request: AdminTenantTierUpdateRequest;
}>;


export type UpdateTenantTierMutation = { updateTenantTier: { id: string, token: string, name: string | null, description: string | null, config: string | null, tenantCount: number, createdAt: string | null, updatedAt: string | null } };

export type DeleteTenantTierMutationVariables = Exact<{
  token: string;
}>;


export type DeleteTenantTierMutation = { deleteTenantTier: boolean };

export type RolesQueryVariables = Exact<{
  scope?: string | null | undefined;
}>;


export type RolesQuery = { roles: Array<{ id: string, scope: string, token: string, name: string | null, description: string | null, authorities: Array<string>, createdAt: string | null, updatedAt: string | null }> };

export type AdminAuditEventsQueryVariables = Exact<{
  criteria: AdminAuditEventSearchCriteria;
}>;


export type AdminAuditEventsQuery = { auditEvents: { results: Array<{ id: string, occurredTime: string, category: string, tenant: string | null, actor: string, operation: string, tableName: string | null, entityPk: string | null, entityLabel: string | null, rowsAffected: number }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AuthoritiesQueryVariables = Exact<{
  scope?: string | null | undefined;
}>;


export type AuthoritiesQuery = { authorities: Array<string> };

export type CreateIdentityMutationVariables = Exact<{
  request: AdminIdentityCreateRequest;
}>;


export type CreateIdentityMutation = { createIdentity: { id: string, email: string, firstName: string | null, lastName: string | null, enabled: boolean, systemRoles: Array<string>, createdAt: string | null, updatedAt: string | null, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type SetIdentityEnabledMutationVariables = Exact<{
  email: string;
  enabled: boolean;
}>;


export type SetIdentityEnabledMutation = { setIdentityEnabled: { id: string, email: string, firstName: string | null, lastName: string | null, enabled: boolean, systemRoles: Array<string>, createdAt: string | null, updatedAt: string | null, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type SetSystemRolesMutationVariables = Exact<{
  email: string;
  roleTokens: Array<string> | string;
}>;


export type SetSystemRolesMutation = { setSystemRoles: { id: string, email: string, firstName: string | null, lastName: string | null, enabled: boolean, systemRoles: Array<string>, createdAt: string | null, updatedAt: string | null, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type SetPasswordMutationVariables = Exact<{
  email: string;
  password: string;
}>;


export type SetPasswordMutation = { setPassword: { id: string, email: string, enabled: boolean } };

export type DeleteIdentityMutationVariables = Exact<{
  email: string;
}>;


export type DeleteIdentityMutation = { deleteIdentity: boolean };

export type AddMembershipMutationVariables = Exact<{
  email: string;
  tenant: string;
  roleTokens: Array<string> | string;
}>;


export type AddMembershipMutation = { addMembership: { id: string, email: string, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type SetMembershipRolesMutationVariables = Exact<{
  email: string;
  tenant: string;
  roleTokens: Array<string> | string;
}>;


export type SetMembershipRolesMutation = { setMembershipRoles: { id: string, email: string, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type SetMembershipEnabledMutationVariables = Exact<{
  email: string;
  tenant: string;
  enabled: boolean;
}>;


export type SetMembershipEnabledMutation = { setMembershipEnabled: { id: string, email: string, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type RemoveMembershipMutationVariables = Exact<{
  email: string;
  tenant: string;
}>;


export type RemoveMembershipMutation = { removeMembership: { id: string, email: string, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> } };

export type CreateRoleMutationVariables = Exact<{
  request: AdminRoleCreateRequest;
}>;


export type CreateRoleMutation = { createRole: { id: string, scope: string, token: string, name: string | null, description: string | null, authorities: Array<string>, createdAt: string | null, updatedAt: string | null } };

export type UpdateRoleMutationVariables = Exact<{
  scope: string;
  token: string;
  request: AdminRoleUpdateRequest;
}>;


export type UpdateRoleMutation = { updateRole: { id: string, scope: string, token: string, name: string | null, description: string | null, authorities: Array<string>, createdAt: string | null, updatedAt: string | null } };

export type DeleteRoleMutationVariables = Exact<{
  scope: string;
  token: string;
}>;


export type DeleteRoleMutation = { deleteRole: boolean };

export type CreateTenantMutationVariables = Exact<{
  request: AdminTenantCreateRequest;
}>;


export type CreateTenantMutation = { createTenant: { id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null } } };

export type UpdateTenantMutationVariables = Exact<{
  token: string;
  request: AdminTenantUpdateRequest;
}>;


export type UpdateTenantMutation = { updateTenant: { id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null } } };

export type SetTenantEnabledMutationVariables = Exact<{
  token: string;
  enabled: boolean;
}>;


export type SetTenantEnabledMutation = { setTenantEnabled: { id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null } } };

export type DeleteTenantMutationVariables = Exact<{
  token: string;
}>;


export type DeleteTenantMutation = { deleteTenant: boolean };

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

export const IdentitiesDocument = new TypedDocumentString(`
    query Identities {
  identities {
    id
    email
    firstName
    lastName
    enabled
    systemRoles
    memberships {
      tenant
      enabled
      roles
    }
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<IdentitiesQuery, IdentitiesQueryVariables>;
export const TenantsDocument = new TypedDocumentString(`
    query Tenants {
  tenants {
    id
    token
    name
    enabled
    tier {
      token
      name
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    effectiveSettings {
      dimension {
        name
        label
        rateUnit
      }
      rate {
        source
        value
        tier
        override
      }
      burst {
        source
        value
        tier
        override
      }
    }
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<TenantsQuery, TenantsQueryVariables>;
export const GovernanceDimensionsDocument = new TypedDocumentString(`
    query GovernanceDimensions {
  governanceDimensions {
    name
    label
    rateField
    burstField
    rateUnit
  }
}
    `) as unknown as TypedDocumentString<GovernanceDimensionsQuery, GovernanceDimensionsQueryVariables>;
export const TenantTiersDocument = new TypedDocumentString(`
    query TenantTiers {
  tenantTiers {
    id
    token
    name
    description
  }
}
    `) as unknown as TypedDocumentString<TenantTiersQuery, TenantTiersQueryVariables>;
export const TenantTierCatalogDocument = new TypedDocumentString(`
    query TenantTierCatalog {
  tenantTiers {
    id
    token
    name
    description
    config
    tenantCount
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<TenantTierCatalogQuery, TenantTierCatalogQueryVariables>;
export const CreateTenantTierDocument = new TypedDocumentString(`
    mutation CreateTenantTier($request: AdminTenantTierCreateRequest!) {
  createTenantTier(request: $request) {
    id
    token
    name
    description
    config
    tenantCount
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<CreateTenantTierMutation, CreateTenantTierMutationVariables>;
export const UpdateTenantTierDocument = new TypedDocumentString(`
    mutation UpdateTenantTier($token: String!, $request: AdminTenantTierUpdateRequest!) {
  updateTenantTier(token: $token, request: $request) {
    id
    token
    name
    description
    config
    tenantCount
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateTenantTierMutation, UpdateTenantTierMutationVariables>;
export const DeleteTenantTierDocument = new TypedDocumentString(`
    mutation DeleteTenantTier($token: String!) {
  deleteTenantTier(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteTenantTierMutation, DeleteTenantTierMutationVariables>;
export const RolesDocument = new TypedDocumentString(`
    query Roles($scope: String) {
  roles(scope: $scope) {
    id
    scope
    token
    name
    description
    authorities
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<RolesQuery, RolesQueryVariables>;
export const AdminAuditEventsDocument = new TypedDocumentString(`
    query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {
  auditEvents(criteria: $criteria) {
    results {
      id
      occurredTime
      category
      tenant
      actor
      operation
      tableName
      entityPk
      entityLabel
      rowsAffected
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AdminAuditEventsQuery, AdminAuditEventsQueryVariables>;
export const AuthoritiesDocument = new TypedDocumentString(`
    query Authorities($scope: String) {
  authorities(scope: $scope)
}
    `) as unknown as TypedDocumentString<AuthoritiesQuery, AuthoritiesQueryVariables>;
export const CreateIdentityDocument = new TypedDocumentString(`
    mutation CreateIdentity($request: AdminIdentityCreateRequest!) {
  createIdentity(request: $request) {
    id
    email
    firstName
    lastName
    enabled
    systemRoles
    memberships {
      tenant
      enabled
      roles
    }
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<CreateIdentityMutation, CreateIdentityMutationVariables>;
export const SetIdentityEnabledDocument = new TypedDocumentString(`
    mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {
  setIdentityEnabled(email: $email, enabled: $enabled) {
    id
    email
    firstName
    lastName
    enabled
    systemRoles
    memberships {
      tenant
      enabled
      roles
    }
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<SetIdentityEnabledMutation, SetIdentityEnabledMutationVariables>;
export const SetSystemRolesDocument = new TypedDocumentString(`
    mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {
  setSystemRoles(email: $email, roleTokens: $roleTokens) {
    id
    email
    firstName
    lastName
    enabled
    systemRoles
    memberships {
      tenant
      enabled
      roles
    }
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<SetSystemRolesMutation, SetSystemRolesMutationVariables>;
export const SetPasswordDocument = new TypedDocumentString(`
    mutation SetPassword($email: String!, $password: String!) {
  setPassword(email: $email, password: $password) {
    id
    email
    enabled
  }
}
    `) as unknown as TypedDocumentString<SetPasswordMutation, SetPasswordMutationVariables>;
export const DeleteIdentityDocument = new TypedDocumentString(`
    mutation DeleteIdentity($email: String!) {
  deleteIdentity(email: $email)
}
    `) as unknown as TypedDocumentString<DeleteIdentityMutation, DeleteIdentityMutationVariables>;
export const AddMembershipDocument = new TypedDocumentString(`
    mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {
  addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {
    id
    email
    memberships {
      tenant
      enabled
      roles
    }
  }
}
    `) as unknown as TypedDocumentString<AddMembershipMutation, AddMembershipMutationVariables>;
export const SetMembershipRolesDocument = new TypedDocumentString(`
    mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {
  setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {
    id
    email
    memberships {
      tenant
      enabled
      roles
    }
  }
}
    `) as unknown as TypedDocumentString<SetMembershipRolesMutation, SetMembershipRolesMutationVariables>;
export const SetMembershipEnabledDocument = new TypedDocumentString(`
    mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {
  setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {
    id
    email
    memberships {
      tenant
      enabled
      roles
    }
  }
}
    `) as unknown as TypedDocumentString<SetMembershipEnabledMutation, SetMembershipEnabledMutationVariables>;
export const RemoveMembershipDocument = new TypedDocumentString(`
    mutation RemoveMembership($email: String!, $tenant: String!) {
  removeMembership(email: $email, tenant: $tenant) {
    id
    email
    memberships {
      tenant
      enabled
      roles
    }
  }
}
    `) as unknown as TypedDocumentString<RemoveMembershipMutation, RemoveMembershipMutationVariables>;
export const CreateRoleDocument = new TypedDocumentString(`
    mutation CreateRole($request: AdminRoleCreateRequest!) {
  createRole(request: $request) {
    id
    scope
    token
    name
    description
    authorities
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<CreateRoleMutation, CreateRoleMutationVariables>;
export const UpdateRoleDocument = new TypedDocumentString(`
    mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {
  updateRole(scope: $scope, token: $token, request: $request) {
    id
    scope
    token
    name
    description
    authorities
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateRoleMutation, UpdateRoleMutationVariables>;
export const DeleteRoleDocument = new TypedDocumentString(`
    mutation DeleteRole($scope: String!, $token: String!) {
  deleteRole(scope: $scope, token: $token)
}
    `) as unknown as TypedDocumentString<DeleteRoleMutation, DeleteRoleMutationVariables>;
export const CreateTenantDocument = new TypedDocumentString(`
    mutation CreateTenant($request: AdminTenantCreateRequest!) {
  createTenant(request: $request) {
    id
    token
    name
    enabled
    tier {
      token
      name
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<CreateTenantMutation, CreateTenantMutationVariables>;
export const UpdateTenantDocument = new TypedDocumentString(`
    mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {
  updateTenant(token: $token, request: $request) {
    id
    token
    name
    enabled
    tier {
      token
      name
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateTenantMutation, UpdateTenantMutationVariables>;
export const SetTenantEnabledDocument = new TypedDocumentString(`
    mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {
  setTenantEnabled(token: $token, enabled: $enabled) {
    id
    token
    name
    enabled
    tier {
      token
      name
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    createdAt
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<SetTenantEnabledMutation, SetTenantEnabledMutationVariables>;
export const DeleteTenantDocument = new TypedDocumentString(`
    mutation DeleteTenant($token: String!) {
  deleteTenant(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteTenantMutation, DeleteTenantMutationVariables>;