/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type TenantBasemapInput = {
  attribution?: string | null | undefined;
  centerLat?: number | null | undefined;
  centerLon?: number | null | undefined;
  tileUrl?: string | null | undefined;
  zoom?: number | null | undefined;
};

export type TenantBrandingInput = {
  accent?: string | null | undefined;
  background?: string | null | undefined;
  foreground?: string | null | undefined;
  logoMaxHeight?: number | null | undefined;
  primary?: string | null | undefined;
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

export type TenantFieldsFragment = { token: string, name: string | null, description: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } };

export type CurrentTenantQueryVariables = Exact<{ [key: string]: never; }>;


export type CurrentTenantQuery = { tenant: { token: string, name: string | null, description: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantBrandingMutationVariables = Exact<{
  input: TenantBrandingInput;
}>;


export type SetTenantBrandingMutation = { setTenantBranding: { token: string, name: string | null, description: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantLogoMutationVariables = Exact<{
  logo?: string | null | undefined;
}>;


export type SetTenantLogoMutation = { setTenantLogo: { token: string, name: string | null, description: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type SetTenantBasemapMutationVariables = Exact<{
  input: TenantBasemapInput;
}>;


export type SetTenantBasemapMutation = { setTenantBasemap: { token: string, name: string | null, description: string | null, branding: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, brandingOverride: { title: string | null, logo: string | null, logoMaxHeight: number | null, primary: string | null, background: string | null, foreground: string | null, accent: string | null, updatedAt: string | null }, basemap: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null }, basemapOverride: { tileUrl: string | null, attribution: string | null, centerLat: number | null, centerLon: number | null, zoom: number | null } } };

export type MeQueryVariables = Exact<{ [key: string]: never; }>;


export type MeQuery = { me: { email: string, firstName: string | null, lastName: string | null } };

export type UpdateProfileMutationVariables = Exact<{
  firstName?: string | null | undefined;
  lastName?: string | null | undefined;
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
}`) as unknown as TypedDocumentString<SetTenantBasemapMutation, SetTenantBasemapMutationVariables>;
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
    mutation UpdateProfile($firstName: String, $lastName: String) {
  updateProfile(firstName: $firstName, lastName: $lastName) {
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