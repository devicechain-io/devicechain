// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the instance-scoped admin API (ADR-033),
// served by user-management at /admin/graphql. Every call authenticates with the
// identity token ({ identity: true }) rather than a tenant access token, and is
// authorized server-side on a system authority (superusers hold "*").
//
// Selection sets are inlined per operation (matching user-management.ts:
// fragmentMasking is off in codegen, so fragments would only add unused locals).
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management-admin';
import type {
  IdentitiesQuery,
  TenantsQuery,
  TenantTiersQuery,
  TenantTierCatalogQuery,
  GovernanceDimensionsQuery,
  RolesQuery,
  AdminAuditEventsQuery,
  AdminAuditEventSearchCriteria,
  AdminIdentityCreateRequest,
  AdminRoleCreateRequest,
  AdminRoleUpdateRequest,
  AdminTenantCreateRequest,
  AdminTenantUpdateRequest,
  AdminTenantTierCreateRequest,
  AdminTenantTierUpdateRequest,
  CreateTenantMutation,
} from '@/gql/user-management-admin/graphql';

// Public types derive from the generated operation results so they can never
// drift from the schema.
export type AdminIdentity = IdentitiesQuery['identities'][number];
export type AdminMembership = AdminIdentity['memberships'][number];
export type AdminTenant = TenantsQuery['tenants'][number];
export type AdminTenantSetting = AdminTenant['effectiveSettings'][number];
// What the tenant-returning MUTATIONS hand back: the tenant's own record, without
// the resolved effectiveSettings the list query carries. A distinct type because the
// difference is real — a write returns what was written, and re-deriving the whole
// cascade on every mutation would be work for a field no caller of them reads (the
// one screen showing effective settings reloads through listTenants). Callers that
// need the cascade must go read it, not assume a mutation result carries it.
export type AdminTenantRecord = CreateTenantMutation['createTenant'];
export type AdminTenantTier = TenantTiersQuery['tenantTiers'][number];
export type AdminGovernanceDimension = GovernanceDimensionsQuery['governanceDimensions'][number];
export type AdminRole = RolesQuery['roles'][number];
export type AdminAuditEvent = AdminAuditEventsQuery['auditEvents']['results'][number];
export type AdminAuditEventSearchResults = AdminAuditEventsQuery['auditEvents'];

export type {
  AdminAuditEventSearchCriteria,
  AdminIdentityCreateRequest,
  AdminRoleCreateRequest,
  AdminRoleUpdateRequest,
  AdminTenantCreateRequest,
  AdminTenantUpdateRequest,
  AdminTenantTierCreateRequest,
  AdminTenantTierUpdateRequest,
};

// Which level of the ADR-065 cascade produced an effective setting. Mirrors
// iam.SettingSource on the server; the schema carries it as a String rather than an
// enum (the codebase has no enum precedent), so the values are named once here
// rather than spelled at each comparison.
export const SETTING_SOURCE = {
  override: 'override',
  tier: 'tier',
  platformDefault: 'platform-default',
} as const;

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
      # What the tenant is ACTUALLY metered at, with the provenance that makes it
      # readable as tier + delta (ADR-065 decision 7). Resolved server-side by the
      # same cascade the data plane reads its ceilings through — deliberately not
      # recomputed here from the fields above, which would be a second implementation
      # that eventually tells an operator something the platform does not do.
      #
      # Selected only on this query, not on the tenant-returning mutations: the one
      # screen that renders it reads through listTenants and reloads after a write.
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
`);

export async function listTenants(): Promise<AdminTenant[]> {
  const data = await gql('user-management/admin', TENANTS, undefined, { identity: true });
  return data.tenants;
}

// The governance dimensions the platform declares (ADR-023). Enumerated from the
// server rather than listed here, so the tier editor offers exactly the settings a
// tier may carry: a fourth dimension becomes configurable the day it is declared,
// instead of shipping a ceiling no operator can see or set.
const GOVERNANCE_DIMENSIONS = graphql(`
  query GovernanceDimensions {
    governanceDimensions {
      name
      label
      rateField
      burstField
      rateUnit
    }
  }
`);

export async function listGovernanceDimensions(): Promise<AdminGovernanceDimension[]> {
  const data = await gql('user-management/admin', GOVERNANCE_DIMENSIONS, undefined, { identity: true });
  return data.governanceDimensions;
}

// The tier catalog (ADR-065) — the operator-defined packaging a tenant is created
// at. tenantCount is deliberately not selected here: this feeds the tenant form's
// picker, which only needs the vocabulary, and asking for the count would run a
// query per tier to render a dropdown.
const TENANT_TIERS = graphql(`
  query TenantTiers {
    tenantTiers {
      id
      token
      name
      description
    }
  }
`);

export async function listTenantTiers(): Promise<AdminTenantTier[]> {
  const data = await gql('user-management/admin', TENANT_TIERS, undefined, { identity: true });
  return data.tenantTiers;
}

// The tier catalog as the MANAGEMENT screen needs it: settings and tenant count on
// top of the picker's vocabulary. Kept separate from TENANT_TIERS above rather than
// widening it — tenantCount runs a query per tier, which the tenant form's dropdown
// should not pay for.
const TENANT_TIER_CATALOG = graphql(`
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
`);

export type AdminTenantTierDetail = TenantTierCatalogQuery['tenantTiers'][number];

export async function listTenantTierCatalog(): Promise<AdminTenantTierDetail[]> {
  const data = await gql('user-management/admin', TENANT_TIER_CATALOG, undefined, { identity: true });
  return data.tenantTiers;
}

const CREATE_TENANT_TIER = graphql(`
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
`);

export async function createTenantTier(request: AdminTenantTierCreateRequest): Promise<AdminTenantTierDetail> {
  const data = await gql('user-management/admin', CREATE_TENANT_TIER, { request }, { identity: true });
  return data.createTenantTier;
}

const UPDATE_TENANT_TIER = graphql(`
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
`);

// Update a tier. NOTE the asymmetry the server enforces (AdminTenantTierUpdateRequest):
// name/description are a full replace, but config is a PATCH — omitting it leaves the
// tier's settings alone, and only an explicit "{}" clears them. A rename that dropped
// config would silently re-price every tenant at the tier within a minute, so callers
// editing settings must send config explicitly.
export async function updateTenantTier(
  token: string,
  request: AdminTenantTierUpdateRequest,
): Promise<AdminTenantTierDetail> {
  const data = await gql('user-management/admin', UPDATE_TENANT_TIER, { token, request }, { identity: true });
  return data.updateTenantTier;
}

const DELETE_TENANT_TIER = graphql(`
  mutation DeleteTenantTier($token: String!) {
    deleteTenantTier(token: $token)
  }
`);

// Delete a tier; returns whether one was removed. Refused by the server while any
// tenant is still packaged at it — a tenant's tier is a required FK, so there is no
// un-tiered state to strand them in.
export async function deleteTenantTier(token: string): Promise<boolean> {
  const data = await gql('user-management/admin', DELETE_TENANT_TIER, { token }, { identity: true });
  return data.deleteTenantTier;
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

const AUDIT_EVENTS = graphql(`
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
`);

// List the instance's user-management audit journal (auth events + identity /
// role / tenant administration), newest first. Instance-wide (cross-tenant);
// requires audit:read (superusers hold "*").
export async function listAdminAuditEvents(
  criteria: AdminAuditEventSearchCriteria,
): Promise<AdminAuditEventSearchResults> {
  const data = await gql('user-management/admin', AUDIT_EVENTS, { criteria }, { identity: true });
  return data.auditEvents;
}

const AUTHORITIES = graphql(`
  query Authorities {
    authorities
  }
`);

// listAuthorities returns the known authority vocabulary so role forms can offer
// a checklist instead of free-text authority strings.
export async function listAuthorities(): Promise<string[]> {
  const data = await gql('user-management/admin', AUTHORITIES, undefined, { identity: true });
  return data.authorities;
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
`);

export async function createTenant(request: AdminTenantCreateRequest): Promise<AdminTenantRecord> {
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
`);

export async function updateTenant(token: string, request: AdminTenantUpdateRequest): Promise<AdminTenantRecord> {
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
`);

export async function setTenantEnabled(token: string, enabled: boolean): Promise<AdminTenantRecord> {
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
