/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
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

export type LoginMutationVariables = Exact<{
  email: string;
  password: string;
}>;


export type LoginMutation = { login: { identityToken: string, expiresAt: string, superuser: boolean, memberships: Array<{ tenant: string, roles: Array<string> }> } };

export type SelectTenantMutationVariables = Exact<{
  identityToken: string;
  tenant: string;
}>;


export type SelectTenantMutation = { selectTenant: { accessToken: string, refreshToken: string, expiresAt: string } };

export type IdentityMembershipsQueryVariables = Exact<{
  identityToken: string;
}>;


export type IdentityMembershipsQuery = { identityMemberships: Array<{ tenant: string, roles: Array<string> }> };

export type RefreshMutationVariables = Exact<{
  refreshToken: string;
}>;


export type RefreshMutation = { refresh: { accessToken: string, refreshToken: string, expiresAt: string } };

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
export const LoginDocument = new TypedDocumentString(`
    mutation Login($email: String!, $password: String!) {
  login(email: $email, password: $password) {
    identityToken
    expiresAt
    superuser
    memberships {
      tenant
      roles
    }
  }
}
    `) as unknown as TypedDocumentString<LoginMutation, LoginMutationVariables>;
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