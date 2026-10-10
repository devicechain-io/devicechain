/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/**
 * Filters and paging for searching the audit journal. Every filter is optional and filters
 * are combined with AND.
 */
export type AdminAuditEventSearchCriteria = {
  /** Only entries whose actor contains this text. The match ignores case. */
  actor?: string | null | undefined;
  /** Only entries of this category, `auth` or `mutation`. Exact match. */
  category?: string | null | undefined;
  /** Only entries at or before this time, as an RFC 3339 timestamp. */
  endTime?: string | null | undefined;
  /** Only entries of this operation, such as `login` or `update`. Exact match. */
  operation?: string | null | undefined;
  /** Page to return, starting at 1. */
  pageNumber: number;
  /** Results per page. Below 1 means the default of 100; above 1000 is capped at 1000. */
  pageSize: number;
  /** Only entries at or after this time, as an RFC 3339 timestamp. */
  startTime?: string | null | undefined;
  /**
   * Only entries belonging to the tenant with this token. Exact match. Entries that belong
   * to no tenant are excluded when this is set.
   */
  tenant?: string | null | undefined;
};

/** Fields for a new identity. */
export type AdminIdentityCreateRequest = {
  /** The identity's email address. It becomes the sign-in name, is stored lower-cased and cannot be changed. It must not already be in use. */
  email: string;
  /** Whether the identity may sign in. Send false to create it disabled. */
  enabled: boolean;
  /** Given name, shown in the console. A blank value is stored as unset. */
  firstName?: string | null | undefined;
  /** Family name, shown in the console. A blank value is stored as unset. */
  lastName?: string | null | undefined;
  /** The initial password. It must not be empty. It is stored hashed and cannot be read back. */
  password: string;
  /** Tokens of the system roles to assign. Each must name an existing role with system scope; an empty list assigns none. */
  systemRoles: Array<string>;
};

/** Fields for a new role. */
export type AdminRoleCreateRequest = {
  /**
   * The authorities to grant, as listed by the authorities query for the same scope; `*` is
   * allowed at either scope. An unknown authority, or one that belongs to the other scope,
   * rejects the request. An empty list creates a role that grants nothing.
   */
  authorities: Array<string>;
  /** Free-text description. */
  description?: string | null | undefined;
  /** Human-readable name. */
  name?: string | null | undefined;
  /** `system` or `tenant`. A role's scope cannot be changed afterwards. */
  scope: string;
  /** Identifier for the role, unique within its scope. */
  token: string;
};

/**
 * A partial update to a role. Omit a field to keep it, send a value to set it, or send null
 * to clear it. The role is named by the mutation's scope and token arguments, which cannot
 * be changed.
 */
export type AdminRoleUpdateRequest = {
  /**
   * The complete new set of authorities, replacing the current set. Null and an empty list
   * both leave the role granting nothing. Validated as for creation; an unknown authority,
   * or one that belongs to the other scope, rejects the whole update.
   */
  authorities?: Array<string> | null | undefined;
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
};

/** Fields for a new tenant. */
export type AdminTenantCreateRequest = {
  /**
   * Records the tenant's agreement to have its data sent to an external AI model provider.
   * Omit it for not agreed.
   */
  aiExternalEnabled?: boolean | null | undefined;
  /** Override of the AI inference burst allowance, in requests. Must be positive. Omit to inherit. */
  aiInferenceBurst?: number | null | undefined;
  /** Override of the sustained AI inference rate in requests per minute. Must be positive. Omit to inherit. */
  aiInferenceRequestsPerMinute?: number | null | undefined;
  /** Free-form settings as a JSON object serialized to a string. Omit it, or send an empty string or `{}`, for none. */
  config?: string | null | undefined;
  /**
   * Override of the most geofences the tenant may have. A positive whole number of at most
   * 4000; a larger value is rejected, not clamped. Omit to inherit.
   */
  geoFenceCeiling?: number | null | undefined;
  /**
   * Override of the most positions the tenant's geofences may have in total. A positive whole
   * number of at most 128000; a larger value is rejected, not clamped. Omit to inherit.
   */
  geoFencePositionBudget?: number | null | undefined;
  /**
   * Override of the most positions one geofence may have, across all of its rings. A
   * positive whole number of at most 1024; a larger value is rejected, not clamped. Omit to
   * inherit.
   */
  geoFencePositionCeiling?: number | null | undefined;
  /**
   * Override of how many commands may be held waiting for offline devices. Any positive
   * whole number. Omit to inherit the tier's value, then the command service's default.
   */
  heldCommandCeiling?: number | null | undefined;
  /** Override of the ingest burst allowance, in readings. Must be positive. Omit to inherit. */
  ingestBurst?: number | null | undefined;
  /** Override of the sustained ingest rate in readings per second. Must be positive. Omit to inherit the tier's value, then the platform default. */
  ingestMessagesPerSecond?: number | null | undefined;
  /** Human-readable name. */
  name?: string | null | undefined;
  /** Override of the outbound burst allowance, in calls. Must be positive. Omit to inherit. */
  outboundBurst?: number | null | undefined;
  /** Override of the sustained rate of outbound calls in calls per second. Must be positive. Omit to inherit. */
  outboundMessagesPerSecond?: number | null | undefined;
  /**
   * Override of the overload-protection priority: a whole number from 1 to 100, higher
   * meaning the tenant's traffic is refused later when the platform is overloaded; 80 to 100
   * is never refused. Omit to inherit the tier's value, then the platform default.
   */
  shedPriority?: number | null | undefined;
  /** Token of the tier to package the tenant at. The tier must exist. */
  tierToken: string;
  /**
   * Unique identifier for the tenant: letters, digits, hyphens and underscores, starting
   * with a letter or digit, at most 128 characters. It cannot be changed. A token that
   * belongs to a tenant being deleted is reserved and is refused until that deletion finishes.
   */
  token: string;
};

/** Fields for a new tenant tier. */
export type AdminTenantTierCreateRequest = {
  /**
   * A color name from tierColorPalette. Omit it, or send null or an empty string, for no
   * color; an unknown name rejects the request.
   */
  color?: string | null | undefined;
  /**
   * The tier's settings as a JSON object serialized to a string, using the keys described
   * on AdminTenantTier.config. An unknown key, or a value outside its allowed range, rejects
   * the request. Omit it, or send an empty string or `{}`, for a tier with no settings.
   */
  config?: string | null | undefined;
  /** Free-text description. */
  description?: string | null | undefined;
  /** Human-readable name. */
  name?: string | null | undefined;
  /**
   * Unique identifier for the tier: letters, digits, hyphens and underscores, starting with
   * a letter or digit, at most 128 characters.
   */
  token: string;
};

/**
 * A partial update to a tier. Omit a field to keep it, send a value to set it, or send null
 * to clear it. The tier is named by the mutation's token argument. Changes apply to every
 * tenant at the tier.
 */
export type AdminTenantTierUpdateRequest = {
  /**
   * A color name from tierColorPalette, trimmed of surrounding spaces. Null or an empty
   * string clears it; an unknown name rejects the request.
   */
  color?: string | null | undefined;
  /**
   * The tier's settings as a JSON object serialized to a string, replacing the current
   * settings entirely. Null, an empty string or `{}` removes all of them, so every tenant at
   * the tier then falls back to the platform defaults. An unknown key, or a value outside
   * its allowed range, rejects the request.
   */
  config?: string | null | undefined;
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
};

/**
 * A partial update to a tenant. For every field, omitting it keeps the stored value, sending
 * a value sets it, and sending null clears it. Clearing a limit removes the tenant's own
 * override, so the tier's value applies, then the platform default; it never makes the tenant
 * unlimited. The tenant is named by the mutation's token argument. Limits are validated as
 * for creation.
 */
export type AdminTenantUpdateRequest = {
  /**
   * Whether the tenant has agreed to have its data sent to an external AI model provider.
   * Null clears the record, which counts as not agreed.
   */
  aiExternalEnabled?: boolean | null | undefined;
  /** Override of the AI inference burst allowance, in requests. Must be positive. */
  aiInferenceBurst?: number | null | undefined;
  /** Override of the sustained AI inference rate, in requests per minute. Must be positive. */
  aiInferenceRequestsPerMinute?: number | null | undefined;
  /**
   * Free-form settings as a JSON object serialized to a string, replacing the current ones.
   * Null, an empty string or `{}` clears them.
   */
  config?: string | null | undefined;
  /** Override of the most geofences the tenant may have. A positive whole number of at most 4000. */
  geoFenceCeiling?: number | null | undefined;
  /** Override of the most positions the tenant's geofences may have in total. A positive whole number of at most 128000. */
  geoFencePositionBudget?: number | null | undefined;
  /** Override of the most positions one geofence may have. A positive whole number of at most 1024. */
  geoFencePositionCeiling?: number | null | undefined;
  /** Override of how many commands may be held waiting for offline devices. A positive whole number. */
  heldCommandCeiling?: number | null | undefined;
  /** Override of the ingest burst allowance, in readings. Must be positive. */
  ingestBurst?: number | null | undefined;
  /** Override of the sustained ingest rate, in readings per second. Must be positive. */
  ingestMessagesPerSecond?: number | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
  /** Override of the outbound burst allowance, in calls. Must be positive. */
  outboundBurst?: number | null | undefined;
  /** Override of the sustained rate of outbound calls, in calls per second. Must be positive. */
  outboundMessagesPerSecond?: number | null | undefined;
  /** Override of the overload-protection priority, a whole number from 1 to 100. */
  shedPriority?: number | null | undefined;
  /**
   * Token of the tier to move the tenant to; the change takes effect within about a minute.
   * Omit it to keep the current tier. Null is rejected, because every tenant has a tier.
   */
  tierToken?: string | null | undefined;
};

/** What a tenant deletion is currently waiting on. */
export type DeletionWait =
  /** Nothing is outstanding. */
  | 'NONE'
  /** Every storage system is clean but has not yet stayed clean for the required settling period. */
  | 'SETTLE'
  /**
   * At least one storage system has not reported clean. This is the only wait that needs a
   * person; blockedBy says which system and why.
   */
  | 'STORES'
  /**
   * Everything is clean and settled, but the deletion is not yet old enough to release the
   * tenant's token for reuse.
   */
  | 'TOKEN_HOLD';

/** Paging and an optional completion filter for listing tenant deletions. */
export type TenantDeletionSearchCriteria = {
  /** True for deletions that have finished, false for those still in progress. Omit it for both. */
  completed?: boolean | null | undefined;
  /** Page to return, starting at 1. */
  pageNumber: number;
  /** Results per page. Below 1 means the default of 100; above 1000 is capped at 1000. */
  pageSize: number;
};

export type IdentitiesQueryVariables = Exact<{ [key: string]: never; }>;


export type IdentitiesQuery = { identities: Array<{ id: string, email: string, firstName: string | null, lastName: string | null, enabled: boolean, systemRoles: Array<string>, createdAt: string | null, updatedAt: string | null, memberships: Array<{ tenant: string, enabled: boolean, roles: Array<string> }> }> };

export type TenantsQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantsQuery = { tenants: Array<{ id: string, token: string, name: string | null, enabled: boolean, purgeState: string, purgeEpoch: string | null, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, heldCommandCeiling: number | null, shedPriority: number | null, geoFencePositionCeiling: number | null, geoFenceCeiling: number | null, geoFencePositionBudget: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null, color: string | null }, effectiveSettings: Array<{ dimension: { name: string, label: string, rateUnit: string }, rate: { source: string, value: number | null, tier: number | null, override: number | null }, burst: { source: string, value: number | null, tier: number | null, override: number | null } }> }> };

export type GovernanceDimensionsQueryVariables = Exact<{ [key: string]: never; }>;


export type GovernanceDimensionsQuery = { governanceDimensions: Array<{ name: string, label: string, rateField: string, burstField: string, rateUnit: string }> };

export type TenantTiersQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantTiersQuery = { tenantTiers: Array<{ id: string, token: string, name: string | null, description: string | null, color: string | null, displayOrder: number }> };

export type TenantTierCatalogQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantTierCatalogQuery = { tenantTiers: Array<{ id: string, token: string, name: string | null, description: string | null, config: string | null, color: string | null, displayOrder: number, tenantCount: number, createdAt: string | null, updatedAt: string | null }> };

export type CreateTenantTierMutationVariables = Exact<{
  request: AdminTenantTierCreateRequest;
}>;


export type CreateTenantTierMutation = { createTenantTier: { id: string, token: string, name: string | null, description: string | null, config: string | null, color: string | null, displayOrder: number, tenantCount: number, createdAt: string | null, updatedAt: string | null } };

export type UpdateTenantTierMutationVariables = Exact<{
  token: string;
  request: AdminTenantTierUpdateRequest;
}>;


export type UpdateTenantTierMutation = { updateTenantTier: { id: string, token: string, name: string | null, description: string | null, config: string | null, color: string | null, displayOrder: number, tenantCount: number, createdAt: string | null, updatedAt: string | null } };

export type DeleteTenantTierMutationVariables = Exact<{
  token: string;
}>;


export type DeleteTenantTierMutation = { deleteTenantTier: boolean };

export type TierColorPaletteQueryVariables = Exact<{ [key: string]: never; }>;


export type TierColorPaletteQuery = { tierColorPalette: { colors: Array<string> } };

export type ReorderTenantTiersMutationVariables = Exact<{
  orderedTokens: Array<string> | string;
}>;


export type ReorderTenantTiersMutation = { reorderTenantTiers: Array<{ id: string, token: string, displayOrder: number }> };

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


export type CreateTenantMutation = { createTenant: { id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, heldCommandCeiling: number | null, shedPriority: number | null, geoFencePositionCeiling: number | null, geoFenceCeiling: number | null, geoFencePositionBudget: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null, color: string | null } } };

export type UpdateTenantMutationVariables = Exact<{
  token: string;
  request: AdminTenantUpdateRequest;
}>;


export type UpdateTenantMutation = { updateTenant: { id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, heldCommandCeiling: number | null, shedPriority: number | null, geoFencePositionCeiling: number | null, geoFenceCeiling: number | null, geoFencePositionBudget: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null, color: string | null } } };

export type SetTenantEnabledMutationVariables = Exact<{
  token: string;
  enabled: boolean;
}>;


export type SetTenantEnabledMutation = { setTenantEnabled: { id: string, token: string, name: string | null, enabled: boolean, config: string | null, ingestMessagesPerSecond: number | null, ingestBurst: number | null, outboundMessagesPerSecond: number | null, outboundBurst: number | null, aiExternalEnabled: boolean | null, aiInferenceRequestsPerMinute: number | null, aiInferenceBurst: number | null, heldCommandCeiling: number | null, shedPriority: number | null, geoFencePositionCeiling: number | null, geoFenceCeiling: number | null, geoFencePositionBudget: number | null, createdAt: string | null, updatedAt: string | null, tier: { token: string, name: string | null, color: string | null } } };

export type DeleteTenantMutationVariables = Exact<{
  token: string;
}>;


export type DeleteTenantMutation = { deleteTenant: boolean };

export type TenantDeletionQueryVariables = Exact<{
  token: string;
  epoch?: string | null | undefined;
}>;


export type TenantDeletionQuery = { tenantDeletion: { token: string, epoch: string, completedAt: string | null, rowsErased: number, awaiting: DeletionWait, elapsesAt: string | null, blockedBy: Array<string>, stores: Array<{ store: string, complete: boolean, rowsErased: number, retaining: string | null, lastError: string | null, note: string | null, attemptedAt: string | null, cleanSince: string | null }> } | null };

export type TenantDeletionsQueryVariables = Exact<{
  criteria: TenantDeletionSearchCriteria;
}>;


export type TenantDeletionsQuery = { tenantDeletions: { results: Array<{ token: string, epoch: string, completedAt: string | null, rowsErased: number, awaiting: DeletionWait, elapsesAt: string | null, blockedBy: Array<string>, stores: Array<{ store: string, complete: boolean, rowsErased: number, retaining: string | null, lastError: string | null, note: string | null, attemptedAt: string | null, cleanSince: string | null }> }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AdminFunctionalAreasQueryVariables = Exact<{ [key: string]: never; }>;


export type AdminFunctionalAreasQuery = { functionalAreas: Array<string> };

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
    purgeState
    purgeEpoch
    tier {
      token
      name
      color
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    heldCommandCeiling
    shedPriority
    geoFencePositionCeiling
    geoFenceCeiling
    geoFencePositionBudget
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
    color
    displayOrder
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
    color
    displayOrder
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
    color
    displayOrder
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
    color
    displayOrder
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
export const TierColorPaletteDocument = new TypedDocumentString(`
    query TierColorPalette {
  tierColorPalette {
    colors
  }
}
    `) as unknown as TypedDocumentString<TierColorPaletteQuery, TierColorPaletteQueryVariables>;
export const ReorderTenantTiersDocument = new TypedDocumentString(`
    mutation ReorderTenantTiers($orderedTokens: [String!]!) {
  reorderTenantTiers(orderedTokens: $orderedTokens) {
    id
    token
    displayOrder
  }
}
    `) as unknown as TypedDocumentString<ReorderTenantTiersMutation, ReorderTenantTiersMutationVariables>;
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
      color
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    heldCommandCeiling
    shedPriority
    geoFencePositionCeiling
    geoFenceCeiling
    geoFencePositionBudget
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
      color
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    heldCommandCeiling
    shedPriority
    geoFencePositionCeiling
    geoFenceCeiling
    geoFencePositionBudget
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
      color
    }
    config
    ingestMessagesPerSecond
    ingestBurst
    outboundMessagesPerSecond
    outboundBurst
    aiExternalEnabled
    aiInferenceRequestsPerMinute
    aiInferenceBurst
    heldCommandCeiling
    shedPriority
    geoFencePositionCeiling
    geoFenceCeiling
    geoFencePositionBudget
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
export const TenantDeletionDocument = new TypedDocumentString(`
    query TenantDeletion($token: String!, $epoch: String) {
  tenantDeletion(token: $token, epoch: $epoch) {
    token
    epoch
    completedAt
    rowsErased
    awaiting
    elapsesAt
    blockedBy
    stores {
      store
      complete
      rowsErased
      retaining
      lastError
      note
      attemptedAt
      cleanSince
    }
  }
}
    `) as unknown as TypedDocumentString<TenantDeletionQuery, TenantDeletionQueryVariables>;
export const TenantDeletionsDocument = new TypedDocumentString(`
    query TenantDeletions($criteria: TenantDeletionSearchCriteria!) {
  tenantDeletions(criteria: $criteria) {
    results {
      token
      epoch
      completedAt
      rowsErased
      awaiting
      elapsesAt
      blockedBy
      stores {
        store
        complete
        rowsErased
        retaining
        lastError
        note
        attemptedAt
        cleanSince
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<TenantDeletionsQuery, TenantDeletionsQueryVariables>;
export const AdminFunctionalAreasDocument = new TypedDocumentString(`
    query AdminFunctionalAreas {
  functionalAreas
}
    `) as unknown as TypedDocumentString<AdminFunctionalAreasQuery, AdminFunctionalAreasQueryVariables>;