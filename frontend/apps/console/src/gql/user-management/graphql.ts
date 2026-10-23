/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** Paging for listing this tenant's invitations. */
export type InvitationSearchCriteria = {
  /** Page to return, starting at 1. */
  pageNumber: number;
  /** Results per page. Below 1 means the default of 100; above 1000 is capped at 1000. */
  pageSize: number;
};

/** Where an invitation is in its life. */
export type InvitationState =
  /** Accepted: the member has joined. */
  | 'ACCEPTED'
  /** Not accepted before it expired. Reissue it to send a new one. */
  | 'EXPIRED'
  /** Issued and still usable. */
  | 'PENDING'
  /** Revoked by an administrator. */
  | 'REVOKED';

/** A person to invite into this tenant. */
export type InviteMemberRequest = {
  /** The person's email address. */
  email: string;
  /** Tokens of the roles to give them. Each must be assignable in this tenant. */
  roleTokens: Array<string>;
  /**
   * The customers to limit them to. Omit it, or send an empty list, for the whole tenant. At
   * most 16 anchors.
   */
  scope?: Array<ScopeAnchorInput> | null | undefined;
};

/** Filters and paging for listing this tenant's members. Filters are combined with AND. */
export type MemberSearchCriteria = {
  /** Only members whose email starts with this text, ignoring case. */
  emailPrefix?: string | null | undefined;
  /** True for enabled memberships only, false for disabled ones only. Omit it for both. */
  enabled?: boolean | null | undefined;
  /**
   * True for memberships the tenant's identity provider manages only, false for manual ones
   * only. Omit it for both.
   */
  managedByIdentityProvider?: boolean | null | undefined;
  /** Page to return, starting at 1. */
  pageNumber: number;
  /** Results per page. Below 1 means the default of 100; above 1000 is capped at 1000. */
  pageSize: number;
  /** Only members who hold the role with this token, whether the instance's or the tenant's own. */
  roleToken?: string | null | undefined;
  /**
   * Only members whose scope names this customer or group. A member with the whole tenant has
   * no scope and is not matched.
   */
  scopeAnchor?: ScopeAnchorInput | null | undefined;
};

/**
 * Changes to the signed-in user's own display name. The user is identified by the request's
 * access token, so only your own profile can be edited. For each field, leaving it out keeps
 * the stored name, a value sets it, and null or an empty string clears it.
 */
export type ProfileUpdateRequest = {
  /** New first name. Omit to keep it, send null or an empty string to clear it. */
  firstName?: string | null | undefined;
  /** New last name. Omit to keep it, send null or an empty string to clear it. */
  lastName?: string | null | undefined;
};

/** Who defined a role. */
export type RoleOwner =
  /** A role the instance operator defined. It can be assigned here but not changed here. */
  | 'INSTANCE'
  /** A role this tenant defined for itself. It exists only in this tenant. */
  | 'TENANT';

/** One entry of a scope to set on a member. */
export type ScopeAnchorInput = {
  /** What the anchor names. */
  kind: ScopeAnchorKind;
  /** Token of the customer or group the anchor names. */
  token: string;
};

/** The kind of thing a scope anchor names. */
export type ScopeAnchorKind =
  /** A customer: the anchor covers the devices assigned to that customer. */
  | 'CUSTOMER'
  /**
   * An entity group: the anchor covers the devices in the group while it is an access scope (at
   * its active version), so a device moved into or out of the group is seen or not without the
   * member being edited. A group that is not an access scope covers nothing.
   */
  | 'GROUP';

/** A service account to create. */
export type ServiceAccountCreateRequest = {
  /** Free-text description, at most 1024 characters. */
  description?: string | null | undefined;
  /** Human-readable name, at most 128 characters. */
  name?: string | null | undefined;
  /**
   * The roles to assign. Each must be assignable in this tenant and grant only authorities
   * you hold yourself.
   */
  roleTokens: Array<string>;
  /** The customers or groups to limit the account to. Omit it, or send an empty list, for the whole tenant. */
  scope?: Array<ScopeAnchorInput> | null | undefined;
  /** Days until the first secret expires: 1 to 730. Defaults to 365. */
  secretExpiresInDays?: number | null | undefined;
  /**
   * Unique identifier within this tenant: letters, digits, hyphens and underscores, starting
   * with a letter or digit, at most 128 characters.
   */
  token: string;
};

/** Paging for listing this tenant's service accounts. */
export type ServiceAccountSearchCriteria = {
  /** Page to return, starting at 1. */
  pageNumber: number;
  /** Results per page. Below 1 means the default of 100; above 1000 is capped at 1000. */
  pageSize: number;
};

/**
 * A basemap for setTenantBasemap. Every field is optional and the input replaces the
 * tenant's whole basemap: a field left out or sent as null (or as a blank string) is cleared,
 * so that aspect falls back to the instance default.
 */
export type TenantBasemapInput = {
  /**
   * Credit line for the tiles, at most 512 characters. The only markup allowed is links
   * written exactly as `<a href="https://...">text</a>`. Required when tileUrl is set, and
   * rejected without it.
   */
  attribution?: string | null | undefined;
  /** Latitude in degrees, from -90 to 90. Must be sent together with centerLon. */
  centerLat?: number | null | undefined;
  /** Longitude in degrees, from -180 to 180. Must be sent together with centerLat. */
  centerLon?: number | null | undefined;
  /**
   * Raster tile URL template: an https URL of at most 2048 characters containing {z}, {x}
   * and {y}, or {bbox-epsg-3857}, or {quadkey}. {prefix} and {ratio} are also substituted;
   * any other placeholder is rejected. Requires attribution.
   */
  tileUrl?: string | null | undefined;
  /** Zoom level, from 0 to 24. */
  zoom?: number | null | undefined;
};

/**
 * A tenant's white-labeling theme for setTenantBranding. It replaces the whole theme: a
 * field left out or sent as null is cleared, so that aspect falls back to the instance
 * default. The logo is not part of it; see setTenantLogo.
 */
export type TenantBrandingInput = {
  /** Accent color as a hex string, `#rrggbb`. */
  accent?: string | null | undefined;
  /** Page background color as a hex string, `#rrggbb`. */
  background?: string | null | undefined;
  /** Text color as a hex string, `#rrggbb`. */
  foreground?: string | null | undefined;
  /** Maximum height in pixels at which the logo is drawn; from 16 to 200. */
  logoMaxHeight?: number | null | undefined;
  /** Primary brand color as a hex string, `#rrggbb`. */
  primary?: string | null | undefined;
  /** Product name shown in the browser tab and the console, at most 64 characters. */
  title?: string | null | undefined;
};

/** A role to define for this tenant. */
export type TenantRoleCreateRequest = {
  /**
   * The authorities the role grants. Each must be a tenant authority that you hold yourself;
   * `*` only if you hold `*`.
   */
  authorities: Array<string>;
  /** Free-text description of the role. */
  description?: string | null | undefined;
  /** Human-readable name of the role. */
  name?: string | null | undefined;
  /**
   * Unique identifier for the role: letters, digits, hyphens and underscores, starting with a
   * letter or digit, at most 128 characters. It may not be the token of an instance role.
   */
  token: string;
};

/**
 * Changes to a role this tenant defined. For each field, leaving it out keeps the stored value,
 * a value sets it, and null clears it.
 */
export type TenantRoleUpdateRequest = {
  /**
   * New authorities, replacing the current set. Omit to keep them; null or an empty list
   * removes them all. Each must be a tenant authority that you hold yourself.
   */
  authorities?: Array<string> | null | undefined;
  /** New description. Omit to keep it, send null to clear it. */
  description?: string | null | undefined;
  /** New name. Omit to keep it, send null to clear it. */
  name?: string | null | undefined;
};

export type MembersQueryVariables = Exact<{
  criteria: MemberSearchCriteria;
}>;


export type MembersQuery = { members: { results: Array<{ email: string, firstName: string | null, lastName: string | null, enabled: boolean, managedByIdentityProvider: boolean, invitationPending: boolean, roles: Array<{ token: string, name: string | null, owner: RoleOwner }>, scope: Array<{ kind: ScopeAnchorKind, token: string }> }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type InvitationsQueryVariables = Exact<{
  criteria: InvitationSearchCriteria;
}>;


export type InvitationsQuery = { invitations: { results: Array<{ email: string, state: InvitationState, createdAt: string, expiresAt: string, acceptedAt: string | null, revokedAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type TenantRolesQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantRolesQuery = { tenantRoles: Array<{ token: string, name: string | null, description: string | null, authorities: Array<string>, owner: RoleOwner }> };

export type TenantAuthoritiesQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantAuthoritiesQuery = { tenantAuthorities: Array<string> };

export type InviteMemberMutationVariables = Exact<{
  request: InviteMemberRequest;
}>;


export type InviteMemberMutation = { inviteMember: { invitationCode: string, member: { email: string } } };

export type SetMemberRolesMutationVariables = Exact<{
  email: string;
  roleTokens: Array<string> | string;
}>;


export type SetMemberRolesMutation = { setMemberRoles: { email: string } };

export type SetMemberScopeMutationVariables = Exact<{
  email: string;
  scope: Array<ScopeAnchorInput> | ScopeAnchorInput;
}>;


export type SetMemberScopeMutation = { setMemberScope: { email: string } };

export type SetMemberAccessMutationVariables = Exact<{
  email: string;
  roleTokens: Array<string> | string;
  scope: Array<ScopeAnchorInput> | ScopeAnchorInput;
}>;


export type SetMemberAccessMutation = { setMemberAccess: { email: string } };

export type SetMemberEnabledMutationVariables = Exact<{
  email: string;
  enabled: boolean;
}>;


export type SetMemberEnabledMutation = { setMemberEnabled: { email: string } };

export type RemoveMemberMutationVariables = Exact<{
  email: string;
}>;


export type RemoveMemberMutation = { removeMember: boolean };

export type RevokeInvitationMutationVariables = Exact<{
  email: string;
}>;


export type RevokeInvitationMutation = { revokeInvitation: { email: string } };

export type ReissueInvitationMutationVariables = Exact<{
  email: string;
}>;


export type ReissueInvitationMutation = { reissueInvitation: { invitationCode: string, member: { email: string } } };

export type CreateTenantRoleMutationVariables = Exact<{
  request: TenantRoleCreateRequest;
}>;


export type CreateTenantRoleMutation = { createTenantRole: { token: string } };

export type UpdateTenantRoleMutationVariables = Exact<{
  token: string;
  request: TenantRoleUpdateRequest;
}>;


export type UpdateTenantRoleMutation = { updateTenantRole: { token: string } };

export type DeleteTenantRoleMutationVariables = Exact<{
  token: string;
}>;


export type DeleteTenantRoleMutation = { deleteTenantRole: boolean };

export type AcceptInvitationMutationVariables = Exact<{
  code: string;
  password?: string | null | undefined;
  identityToken?: string | null | undefined;
}>;


export type AcceptInvitationMutation = { acceptInvitation: { identityToken: string, expiresAt: string, superuser: boolean, memberships: Array<{ tenant: string, roles: Array<string>, ssoRequired: boolean }> } };

export type ServiceAccountsQueryVariables = Exact<{
  criteria: ServiceAccountSearchCriteria;
}>;


export type ServiceAccountsQuery = { serviceAccountTokenEndpoint: string | null, serviceAccounts: { results: Array<{ token: string, name: string | null, description: string | null, clientId: string, enabled: boolean, createdBy: string | null, createdAt: string, roles: Array<{ token: string, name: string | null, owner: RoleOwner }>, scope: Array<{ kind: ScopeAnchorKind, token: string }>, secrets: Array<{ hint: string, createdAt: string, expiresAt: string, lastUsedAt: string | null, active: boolean }> }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateServiceAccountMutationVariables = Exact<{
  request: ServiceAccountCreateRequest;
}>;


export type CreateServiceAccountMutation = { createServiceAccount: { clientId: string, clientSecret: string, tokenEndpoint: string | null, serviceAccount: { token: string } } };

export type SetServiceAccountAccessMutationVariables = Exact<{
  token: string;
  roleTokens: Array<string> | string;
  scope: Array<ScopeAnchorInput> | ScopeAnchorInput;
}>;


export type SetServiceAccountAccessMutation = { setServiceAccountAccess: { token: string } };

export type SetServiceAccountEnabledMutationVariables = Exact<{
  token: string;
  enabled: boolean;
}>;


export type SetServiceAccountEnabledMutation = { setServiceAccountEnabled: { token: string } };

export type RotateServiceAccountSecretMutationVariables = Exact<{
  token: string;
  overlapHours?: number | null | undefined;
  expiresInDays?: number | null | undefined;
}>;


export type RotateServiceAccountSecretMutation = { rotateServiceAccountSecret: { clientId: string, clientSecret: string, tokenEndpoint: string | null, serviceAccount: { token: string } } };

export type DeleteServiceAccountMutationVariables = Exact<{
  token: string;
}>;


export type DeleteServiceAccountMutation = { deleteServiceAccount: boolean };

export type LoginMutationVariables = Exact<{
  email: string;
  password: string;
}>;


export type LoginMutation = { login: { identityToken: string, expiresAt: string, superuser: boolean, memberships: Array<{ tenant: string, roles: Array<string>, ssoRequired: boolean }> } };

export type BeginSsoMutationVariables = Exact<{
  email?: string | null | undefined;
  connection?: string | null | undefined;
  returnTo: string;
  codeChallenge: string;
}>;


export type BeginSsoMutation = { beginSso: { authorizationUrl: string } | null };

export type CompleteSsoMutationVariables = Exact<{
  code: string;
  codeVerifier: string;
}>;


export type CompleteSsoMutation = { completeSso: { identityToken: string, expiresAt: string, superuser: boolean, memberships: Array<{ tenant: string, roles: Array<string>, ssoRequired: boolean }> } };

export type BeginSsoLinkMutationVariables = Exact<{
  identityToken: string;
  returnTo: string;
}>;


export type BeginSsoLinkMutation = { beginSsoLink: { authorizationUrl: string } };

export type SelectTenantMutationVariables = Exact<{
  identityToken: string;
  tenant: string;
}>;


export type SelectTenantMutation = { selectTenant: { accessToken: string, refreshToken: string, expiresAt: string } };

export type IdentityMembershipsQueryVariables = Exact<{
  identityToken: string;
}>;


export type IdentityMembershipsQuery = { identityMemberships: Array<{ tenant: string, roles: Array<string>, ssoRequired: boolean }> };

export type RefreshMutationVariables = Exact<{
  refreshToken: string;
}>;


export type RefreshMutation = { refresh: { accessToken: string, refreshToken: string, expiresAt: string } };

export type LogoutMutationVariables = Exact<{
  token: string;
}>;


export type LogoutMutation = { logout: { providerSignOutUrl: string | null } };

export type TenantFieldsFragment = { token: string, name: string | null, description: string | null, locale: string | null, localeOverride: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } };

export type CurrentTenantQueryVariables = Exact<{ [key: string]: never; }>;


export type CurrentTenantQuery = { tenant: { token: string, name: string | null, description: string | null, locale: string | null, localeOverride: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantBrandingMutationVariables = Exact<{
  input: TenantBrandingInput;
}>;


export type SetTenantBrandingMutation = { setTenantBranding: { token: string, name: string | null, description: string | null, locale: string | null, localeOverride: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantLogoMutationVariables = Exact<{
  logo?: string | null | undefined;
}>;


export type SetTenantLogoMutation = { setTenantLogo: { token: string, name: string | null, description: string | null, locale: string | null, localeOverride: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantBasemapMutationVariables = Exact<{
  input: TenantBasemapInput;
}>;


export type SetTenantBasemapMutation = { setTenantBasemap: { token: string, name: string | null, description: string | null, locale: string | null, localeOverride: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantLocaleMutationVariables = Exact<{
  locale?: string | null | undefined;
}>;


export type SetTenantLocaleMutation = { setTenantLocale: { token: string, name: string | null, description: string | null, locale: string | null, localeOverride: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type MeQueryVariables = Exact<{ [key: string]: never; }>;


export type MeQuery = { me: { email: string, firstName: string | null, lastName: string | null } };

export type UpdateProfileMutationVariables = Exact<{
  request: ProfileUpdateRequest;
}>;


export type UpdateProfileMutation = { updateProfile: { email: string, firstName: string | null, lastName: string | null } };

export type FunctionalAreasQueryVariables = Exact<{ [key: string]: never; }>;


export type FunctionalAreasQuery = { functionalAreas: Array<string> };

export type TenantTokenMasksQueryVariables = Exact<{ [key: string]: never; }>;


export type TenantTokenMasksQuery = { tokenMasks: string };

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
export const TenantFieldsFragmentDoc = new TypedDocumentString(`
    fragment TenantFields on Tenant {
  token
  name
  description
  branding {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  brandingOverride {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  basemap {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  basemapOverride {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  locale
  localeOverride
}
    `, {"fragmentName":"TenantFields"}) as unknown as TypedDocumentString<TenantFieldsFragment, unknown>;
export const MembersDocument = new TypedDocumentString(`
    query Members($criteria: MemberSearchCriteria!) {
  members(criteria: $criteria) {
    results {
      email
      firstName
      lastName
      enabled
      roles {
        token
        name
        owner
      }
      scope {
        kind
        token
      }
      managedByIdentityProvider
      invitationPending
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<MembersQuery, MembersQueryVariables>;
export const InvitationsDocument = new TypedDocumentString(`
    query Invitations($criteria: InvitationSearchCriteria!) {
  invitations(criteria: $criteria) {
    results {
      email
      state
      createdAt
      expiresAt
      acceptedAt
      revokedAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<InvitationsQuery, InvitationsQueryVariables>;
export const TenantRolesDocument = new TypedDocumentString(`
    query TenantRoles {
  tenantRoles {
    token
    name
    description
    authorities
    owner
  }
}
    `) as unknown as TypedDocumentString<TenantRolesQuery, TenantRolesQueryVariables>;
export const TenantAuthoritiesDocument = new TypedDocumentString(`
    query TenantAuthorities {
  tenantAuthorities
}
    `) as unknown as TypedDocumentString<TenantAuthoritiesQuery, TenantAuthoritiesQueryVariables>;
export const InviteMemberDocument = new TypedDocumentString(`
    mutation InviteMember($request: InviteMemberRequest!) {
  inviteMember(request: $request) {
    member {
      email
    }
    invitationCode
  }
}
    `) as unknown as TypedDocumentString<InviteMemberMutation, InviteMemberMutationVariables>;
export const SetMemberRolesDocument = new TypedDocumentString(`
    mutation SetMemberRoles($email: String!, $roleTokens: [String!]!) {
  setMemberRoles(email: $email, roleTokens: $roleTokens) {
    email
  }
}
    `) as unknown as TypedDocumentString<SetMemberRolesMutation, SetMemberRolesMutationVariables>;
export const SetMemberScopeDocument = new TypedDocumentString(`
    mutation SetMemberScope($email: String!, $scope: [ScopeAnchorInput!]!) {
  setMemberScope(email: $email, scope: $scope) {
    email
  }
}
    `) as unknown as TypedDocumentString<SetMemberScopeMutation, SetMemberScopeMutationVariables>;
export const SetMemberAccessDocument = new TypedDocumentString(`
    mutation SetMemberAccess($email: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {
  setMemberAccess(email: $email, roleTokens: $roleTokens, scope: $scope) {
    email
  }
}
    `) as unknown as TypedDocumentString<SetMemberAccessMutation, SetMemberAccessMutationVariables>;
export const SetMemberEnabledDocument = new TypedDocumentString(`
    mutation SetMemberEnabled($email: String!, $enabled: Boolean!) {
  setMemberEnabled(email: $email, enabled: $enabled) {
    email
  }
}
    `) as unknown as TypedDocumentString<SetMemberEnabledMutation, SetMemberEnabledMutationVariables>;
export const RemoveMemberDocument = new TypedDocumentString(`
    mutation RemoveMember($email: String!) {
  removeMember(email: $email)
}
    `) as unknown as TypedDocumentString<RemoveMemberMutation, RemoveMemberMutationVariables>;
export const RevokeInvitationDocument = new TypedDocumentString(`
    mutation RevokeInvitation($email: String!) {
  revokeInvitation(email: $email) {
    email
  }
}
    `) as unknown as TypedDocumentString<RevokeInvitationMutation, RevokeInvitationMutationVariables>;
export const ReissueInvitationDocument = new TypedDocumentString(`
    mutation ReissueInvitation($email: String!) {
  reissueInvitation(email: $email) {
    member {
      email
    }
    invitationCode
  }
}
    `) as unknown as TypedDocumentString<ReissueInvitationMutation, ReissueInvitationMutationVariables>;
export const CreateTenantRoleDocument = new TypedDocumentString(`
    mutation CreateTenantRole($request: TenantRoleCreateRequest!) {
  createTenantRole(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateTenantRoleMutation, CreateTenantRoleMutationVariables>;
export const UpdateTenantRoleDocument = new TypedDocumentString(`
    mutation UpdateTenantRole($token: String!, $request: TenantRoleUpdateRequest!) {
  updateTenantRole(token: $token, request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<UpdateTenantRoleMutation, UpdateTenantRoleMutationVariables>;
export const DeleteTenantRoleDocument = new TypedDocumentString(`
    mutation DeleteTenantRole($token: String!) {
  deleteTenantRole(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteTenantRoleMutation, DeleteTenantRoleMutationVariables>;
export const AcceptInvitationDocument = new TypedDocumentString(`
    mutation AcceptInvitation($code: String!, $password: String, $identityToken: String) {
  acceptInvitation(
    code: $code
    password: $password
    identityToken: $identityToken
  ) {
    identityToken
    expiresAt
    superuser
    memberships {
      tenant
      roles
      ssoRequired
    }
  }
}
    `) as unknown as TypedDocumentString<AcceptInvitationMutation, AcceptInvitationMutationVariables>;
export const ServiceAccountsDocument = new TypedDocumentString(`
    query ServiceAccounts($criteria: ServiceAccountSearchCriteria!) {
  serviceAccounts(criteria: $criteria) {
    results {
      token
      name
      description
      clientId
      enabled
      createdBy
      createdAt
      roles {
        token
        name
        owner
      }
      scope {
        kind
        token
      }
      secrets {
        hint
        createdAt
        expiresAt
        lastUsedAt
        active
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
  serviceAccountTokenEndpoint
}
    `) as unknown as TypedDocumentString<ServiceAccountsQuery, ServiceAccountsQueryVariables>;
export const CreateServiceAccountDocument = new TypedDocumentString(`
    mutation CreateServiceAccount($request: ServiceAccountCreateRequest!) {
  createServiceAccount(request: $request) {
    clientId
    clientSecret
    tokenEndpoint
    serviceAccount {
      token
    }
  }
}
    `) as unknown as TypedDocumentString<CreateServiceAccountMutation, CreateServiceAccountMutationVariables>;
export const SetServiceAccountAccessDocument = new TypedDocumentString(`
    mutation SetServiceAccountAccess($token: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {
  setServiceAccountAccess(token: $token, roleTokens: $roleTokens, scope: $scope) {
    token
  }
}
    `) as unknown as TypedDocumentString<SetServiceAccountAccessMutation, SetServiceAccountAccessMutationVariables>;
export const SetServiceAccountEnabledDocument = new TypedDocumentString(`
    mutation SetServiceAccountEnabled($token: String!, $enabled: Boolean!) {
  setServiceAccountEnabled(token: $token, enabled: $enabled) {
    token
  }
}
    `) as unknown as TypedDocumentString<SetServiceAccountEnabledMutation, SetServiceAccountEnabledMutationVariables>;
export const RotateServiceAccountSecretDocument = new TypedDocumentString(`
    mutation RotateServiceAccountSecret($token: String!, $overlapHours: Int, $expiresInDays: Int) {
  rotateServiceAccountSecret(
    token: $token
    overlapHours: $overlapHours
    expiresInDays: $expiresInDays
  ) {
    clientId
    clientSecret
    tokenEndpoint
    serviceAccount {
      token
    }
  }
}
    `) as unknown as TypedDocumentString<RotateServiceAccountSecretMutation, RotateServiceAccountSecretMutationVariables>;
export const DeleteServiceAccountDocument = new TypedDocumentString(`
    mutation DeleteServiceAccount($token: String!) {
  deleteServiceAccount(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteServiceAccountMutation, DeleteServiceAccountMutationVariables>;
export const LoginDocument = new TypedDocumentString(`
    mutation Login($email: String!, $password: String!) {
  login(email: $email, password: $password) {
    identityToken
    expiresAt
    superuser
    memberships {
      tenant
      roles
      ssoRequired
    }
  }
}
    `) as unknown as TypedDocumentString<LoginMutation, LoginMutationVariables>;
export const BeginSsoDocument = new TypedDocumentString(`
    mutation BeginSso($email: String, $connection: String, $returnTo: String!, $codeChallenge: String!) {
  beginSso(
    email: $email
    connection: $connection
    returnTo: $returnTo
    codeChallenge: $codeChallenge
  ) {
    authorizationUrl
  }
}
    `) as unknown as TypedDocumentString<BeginSsoMutation, BeginSsoMutationVariables>;
export const CompleteSsoDocument = new TypedDocumentString(`
    mutation CompleteSso($code: String!, $codeVerifier: String!) {
  completeSso(code: $code, codeVerifier: $codeVerifier) {
    identityToken
    expiresAt
    superuser
    memberships {
      tenant
      roles
      ssoRequired
    }
  }
}
    `) as unknown as TypedDocumentString<CompleteSsoMutation, CompleteSsoMutationVariables>;
export const BeginSsoLinkDocument = new TypedDocumentString(`
    mutation BeginSsoLink($identityToken: String!, $returnTo: String!) {
  beginSsoLink(identityToken: $identityToken, returnTo: $returnTo) {
    authorizationUrl
  }
}
    `) as unknown as TypedDocumentString<BeginSsoLinkMutation, BeginSsoLinkMutationVariables>;
export const SelectTenantDocument = new TypedDocumentString(`
    mutation SelectTenant($identityToken: String!, $tenant: String!) {
  selectTenant(identityToken: $identityToken, tenant: $tenant) {
    accessToken
    refreshToken
    expiresAt
  }
}
    `) as unknown as TypedDocumentString<SelectTenantMutation, SelectTenantMutationVariables>;
export const IdentityMembershipsDocument = new TypedDocumentString(`
    query IdentityMemberships($identityToken: String!) {
  identityMemberships(identityToken: $identityToken) {
    tenant
    roles
    ssoRequired
  }
}
    `) as unknown as TypedDocumentString<IdentityMembershipsQuery, IdentityMembershipsQueryVariables>;
export const RefreshDocument = new TypedDocumentString(`
    mutation Refresh($refreshToken: String!) {
  refresh(refreshToken: $refreshToken) {
    accessToken
    refreshToken
    expiresAt
  }
}
    `) as unknown as TypedDocumentString<RefreshMutation, RefreshMutationVariables>;
export const LogoutDocument = new TypedDocumentString(`
    mutation Logout($token: String!) {
  logout(token: $token, returnTo: CONSOLE) {
    providerSignOutUrl
  }
}
    `) as unknown as TypedDocumentString<LogoutMutation, LogoutMutationVariables>;
export const CurrentTenantDocument = new TypedDocumentString(`
    query CurrentTenant {
  tenant {
    ...TenantFields
  }
}
    fragment TenantFields on Tenant {
  token
  name
  description
  branding {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  brandingOverride {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  basemap {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  basemapOverride {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  locale
  localeOverride
}`) as unknown as TypedDocumentString<CurrentTenantQuery, CurrentTenantQueryVariables>;
export const SetTenantBrandingDocument = new TypedDocumentString(`
    mutation SetTenantBranding($input: TenantBrandingInput!) {
  setTenantBranding(input: $input) {
    ...TenantFields
  }
}
    fragment TenantFields on Tenant {
  token
  name
  description
  branding {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  brandingOverride {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  basemap {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  basemapOverride {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  locale
  localeOverride
}`) as unknown as TypedDocumentString<SetTenantBrandingMutation, SetTenantBrandingMutationVariables>;
export const SetTenantLogoDocument = new TypedDocumentString(`
    mutation SetTenantLogo($logo: String) {
  setTenantLogo(logo: $logo) {
    ...TenantFields
  }
}
    fragment TenantFields on Tenant {
  token
  name
  description
  branding {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  brandingOverride {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  basemap {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  basemapOverride {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  locale
  localeOverride
}`) as unknown as TypedDocumentString<SetTenantLogoMutation, SetTenantLogoMutationVariables>;
export const SetTenantBasemapDocument = new TypedDocumentString(`
    mutation SetTenantBasemap($input: TenantBasemapInput!) {
  setTenantBasemap(input: $input) {
    ...TenantFields
  }
}
    fragment TenantFields on Tenant {
  token
  name
  description
  branding {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  brandingOverride {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  basemap {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  basemapOverride {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  locale
  localeOverride
}`) as unknown as TypedDocumentString<SetTenantBasemapMutation, SetTenantBasemapMutationVariables>;
export const SetTenantLocaleDocument = new TypedDocumentString(`
    mutation SetTenantLocale($locale: String) {
  setTenantLocale(locale: $locale) {
    ...TenantFields
  }
}
    fragment TenantFields on Tenant {
  token
  name
  description
  branding {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  brandingOverride {
    title
    logo
    logoMaxHeight
    primary
    background
    foreground
    accent
    updatedAt
  }
  basemap {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  basemapOverride {
    tileUrl
    attribution
    centerLat
    centerLon
    zoom
  }
  locale
  localeOverride
}`) as unknown as TypedDocumentString<SetTenantLocaleMutation, SetTenantLocaleMutationVariables>;
export const MeDocument = new TypedDocumentString(`
    query Me {
  me {
    email
    firstName
    lastName
  }
}
    `) as unknown as TypedDocumentString<MeQuery, MeQueryVariables>;
export const UpdateProfileDocument = new TypedDocumentString(`
    mutation UpdateProfile($request: ProfileUpdateRequest!) {
  updateProfile(request: $request) {
    email
    firstName
    lastName
  }
}
    `) as unknown as TypedDocumentString<UpdateProfileMutation, UpdateProfileMutationVariables>;
export const FunctionalAreasDocument = new TypedDocumentString(`
    query FunctionalAreas {
  functionalAreas
}
    `) as unknown as TypedDocumentString<FunctionalAreasQuery, FunctionalAreasQueryVariables>;
export const TenantTokenMasksDocument = new TypedDocumentString(`
    query TenantTokenMasks {
  tokenMasks
}
    `) as unknown as TypedDocumentString<TenantTokenMasksQuery, TenantTokenMasksQueryVariables>;