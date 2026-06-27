// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the instance-scoped admin API (ADR-033),
// served by user-management at /admin/graphql. Every call authenticates with the
// identity token ({ identity: true }) rather than a tenant access token, and is
// authorized server-side on a system authority (superusers hold "*").
//
// Selection sets are inlined per operation (matching user-management.ts:
// fragmentMasking is off in codegen, so fragments would only add unused locals).
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/user-management-admin';
import type {
  IdentitiesQuery,
  TenantsQuery,
  RolesQuery,
  AdminIdentityCreateRequest,
  AdminRoleCreateRequest,
  AdminRoleUpdateRequest,
  AdminTenantCreateRequest,
  AdminTenantUpdateRequest,
} from '@/gql/user-management-admin/graphql';

// Public types derive from the generated operation results so they can never
// drift from the schema.
export type AdminIdentity = IdentitiesQuery['identities'][number];
export type AdminMembership = AdminIdentity['memberships'][number];
export type AdminTenant = TenantsQuery['tenants'][number];
export type AdminRole = RolesQuery['roles'][number];

export type {
  AdminIdentityCreateRequest,
  AdminRoleCreateRequest,
  AdminRoleUpdateRequest,
  AdminTenantCreateRequest,
  AdminTenantUpdateRequest,
};

// ── Queries ───────────────────────────────────────────────────────────────

const IDENTITIES = graphql(`
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
`);

export async function listIdentities(): Promise<AdminIdentity[]> {
  const data = await gql('user-management/admin', IDENTITIES, undefined, { identity: true });
  return data.identities;
}

const TENANTS = graphql(`
  query Tenants {
    tenants {
      id
      token
      name
      enabled
      config
      createdAt
      updatedAt
    }
  }
`);

export async function listTenants(): Promise<AdminTenant[]> {
  const data = await gql('user-management/admin', TENANTS, undefined, { identity: true });
  return data.tenants;
}

const ROLES = graphql(`
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
`);

export async function listRoles(scope?: 'system' | 'tenant'): Promise<AdminRole[]> {
  const data = await gql('user-management/admin', ROLES, { scope }, { identity: true });
  return data.roles;
}

// ── Identity mutations ──────────────────────────────────────────────────
//
// Every identity-returning mutation selects the same identity shape as the
// Identities query, so the caller can refresh its row from the result.

const CREATE_IDENTITY = graphql(`
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
`);

export async function createIdentity(request: AdminIdentityCreateRequest): Promise<AdminIdentity> {
  const data = await gql('user-management/admin', CREATE_IDENTITY, { request }, { identity: true });
  return data.createIdentity;
}

const SET_IDENTITY_ENABLED = graphql(`
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
`);

export async function setIdentityEnabled(email: string, enabled: boolean): Promise<AdminIdentity> {
  const data = await gql('user-management/admin', SET_IDENTITY_ENABLED, { email, enabled }, { identity: true });
  return data.setIdentityEnabled;
}

const SET_SYSTEM_ROLES = graphql(`
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
`);

export async function setSystemRoles(email: string, roleTokens: string[]): Promise<AdminIdentity> {
  const data = await gql('user-management/admin', SET_SYSTEM_ROLES, { email, roleTokens }, { identity: true });
  return data.setSystemRoles;
}

const SET_PASSWORD = graphql(`
  mutation SetPassword($email: String!, $password: String!) {
    setPassword(email: $email, password: $password) {
      id
      email
      enabled
    }
  }
`);

export async function setPassword(email: string, password: string): Promise<{ id: string; email: string; enabled: boolean }> {
  const data = await gql('user-management/admin', SET_PASSWORD, { email, password }, { identity: true });
  return data.setPassword;
}

const DELETE_IDENTITY = graphql(`
  mutation DeleteIdentity($email: String!) {
    deleteIdentity(email: $email)
  }
`);

export async function deleteIdentity(email: string): Promise<boolean> {
  const data = await gql('user-management/admin', DELETE_IDENTITY, { email }, { identity: true });
  return data.deleteIdentity;
}

// ── Membership mutations ────────────────────────────────────────────────

const ADD_MEMBERSHIP = graphql(`
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
`);

export async function addMembership(email: string, tenant: string, roleTokens: string[]): Promise<AdminIdentity['memberships']> {
  const data = await gql('user-management/admin', ADD_MEMBERSHIP, { email, tenant, roleTokens }, { identity: true });
  return data.addMembership.memberships;
}

const SET_MEMBERSHIP_ROLES = graphql(`
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
`);

export async function setMembershipRoles(email: string, tenant: string, roleTokens: string[]): Promise<AdminIdentity['memberships']> {
  const data = await gql('user-management/admin', SET_MEMBERSHIP_ROLES, { email, tenant, roleTokens }, { identity: true });
  return data.setMembershipRoles.memberships;
}

const SET_MEMBERSHIP_ENABLED = graphql(`
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
`);

export async function setMembershipEnabled(email: string, tenant: string, enabled: boolean): Promise<AdminIdentity['memberships']> {
  const data = await gql('user-management/admin', SET_MEMBERSHIP_ENABLED, { email, tenant, enabled }, { identity: true });
  return data.setMembershipEnabled.memberships;
}

const REMOVE_MEMBERSHIP = graphql(`
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
`);

export async function removeMembership(email: string, tenant: string): Promise<AdminIdentity['memberships']> {
  const data = await gql('user-management/admin', REMOVE_MEMBERSHIP, { email, tenant }, { identity: true });
  return data.removeMembership.memberships;
}

// ── Role mutations ──────────────────────────────────────────────────────

const CREATE_ROLE = graphql(`
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
`);

export async function createRole(request: AdminRoleCreateRequest): Promise<AdminRole> {
  const data = await gql('user-management/admin', CREATE_ROLE, { request }, { identity: true });
  return data.createRole;
}

const UPDATE_ROLE = graphql(`
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
`);

export async function updateRole(
  scope: string,
  token: string,
  request: AdminRoleUpdateRequest,
): Promise<AdminRole> {
  const data = await gql('user-management/admin', UPDATE_ROLE, { scope, token, request }, { identity: true });
  return data.updateRole;
}

const DELETE_ROLE = graphql(`
  mutation DeleteRole($scope: String!, $token: String!) {
    deleteRole(scope: $scope, token: $token)
  }
`);

export async function deleteRole(scope: string, token: string): Promise<boolean> {
  const data = await gql('user-management/admin', DELETE_ROLE, { scope, token }, { identity: true });
  return data.deleteRole;
}

// ── Tenant mutations ────────────────────────────────────────────────────

const CREATE_TENANT = graphql(`
  mutation CreateTenant($request: AdminTenantCreateRequest!) {
    createTenant(request: $request) {
      id
      token
      name
      enabled
      config
      createdAt
      updatedAt
    }
  }
`);

export async function createTenant(request: AdminTenantCreateRequest): Promise<AdminTenant> {
  const data = await gql('user-management/admin', CREATE_TENANT, { request }, { identity: true });
  return data.createTenant;
}

const UPDATE_TENANT = graphql(`
  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {
    updateTenant(token: $token, request: $request) {
      id
      token
      name
      enabled
      config
      createdAt
      updatedAt
    }
  }
`);

export async function updateTenant(token: string, request: AdminTenantUpdateRequest): Promise<AdminTenant> {
  const data = await gql('user-management/admin', UPDATE_TENANT, { token, request }, { identity: true });
  return data.updateTenant;
}

const SET_TENANT_ENABLED = graphql(`
  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {
    setTenantEnabled(token: $token, enabled: $enabled) {
      id
      token
      name
      enabled
      config
      createdAt
      updatedAt
    }
  }
`);

export async function setTenantEnabled(token: string, enabled: boolean): Promise<AdminTenant> {
  const data = await gql('user-management/admin', SET_TENANT_ENABLED, { token, enabled }, { identity: true });
  return data.setTenantEnabled;
}

const DELETE_TENANT = graphql(`
  mutation DeleteTenant($token: String!) {
    deleteTenant(token: $token)
  }
`);

export async function deleteTenant(token: string): Promise<boolean> {
  const data = await gql('user-management/admin', DELETE_TENANT, { token }, { identity: true });
  return data.deleteTenant;
}
