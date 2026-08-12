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
    "\n  query IdentityMemberships($identityToken: String!) {\n    identityMemberships(identityToken: $identityToken) {\n      tenant\n      roles\n    }\n  }\n": typeof types.IdentityMembershipsDocument,
    "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": typeof types.RefreshDocument,
    "\n  fragment TenantFields on Tenant {\n    token\n    name\n    description\n    branding {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    brandingOverride {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    basemap {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    basemapOverride {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n  }\n": typeof types.TenantFieldsFragmentDoc,
    "\n  query CurrentTenant {\n    tenant {\n      ...TenantFields\n    }\n  }\n": typeof types.CurrentTenantDocument,
    "\n  mutation SetTenantBranding($input: TenantBrandingInput!) {\n    setTenantBranding(input: $input) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantBrandingDocument,
    "\n  mutation SetTenantLogo($logo: String) {\n    setTenantLogo(logo: $logo) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantLogoDocument,
    "\n  mutation SetTenantBasemap($input: TenantBasemapInput!) {\n    setTenantBasemap(input: $input) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantBasemapDocument,
    "\n  query Me {\n    me {\n      email\n      firstName\n      lastName\n    }\n  }\n": typeof types.MeDocument,
    "\n  mutation UpdateProfile($firstName: String, $lastName: String) {\n    updateProfile(firstName: $firstName, lastName: $lastName) {\n      email\n      firstName\n      lastName\n    }\n  }\n": typeof types.UpdateProfileDocument,
    "\n  query FunctionalAreas {\n    functionalAreas\n  }\n": typeof types.FunctionalAreasDocument,
};
const documents: Documents = {
    "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n      }\n    }\n  }\n": types.LoginDocument,
    "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": types.SelectTenantDocument,
    "\n  query IdentityMemberships($identityToken: String!) {\n    identityMemberships(identityToken: $identityToken) {\n      tenant\n      roles\n    }\n  }\n": types.IdentityMembershipsDocument,
    "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": types.RefreshDocument,
    "\n  fragment TenantFields on Tenant {\n    token\n    name\n    description\n    branding {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    brandingOverride {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    basemap {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    basemapOverride {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n  }\n": types.TenantFieldsFragmentDoc,
    "\n  query CurrentTenant {\n    tenant {\n      ...TenantFields\n    }\n  }\n": types.CurrentTenantDocument,
    "\n  mutation SetTenantBranding($input: TenantBrandingInput!) {\n    setTenantBranding(input: $input) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantBrandingDocument,
    "\n  mutation SetTenantLogo($logo: String) {\n    setTenantLogo(logo: $logo) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantLogoDocument,
    "\n  mutation SetTenantBasemap($input: TenantBasemapInput!) {\n    setTenantBasemap(input: $input) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantBasemapDocument,
    "\n  query Me {\n    me {\n      email\n      firstName\n      lastName\n    }\n  }\n": types.MeDocument,
    "\n  mutation UpdateProfile($firstName: String, $lastName: String) {\n    updateProfile(firstName: $firstName, lastName: $lastName) {\n      email\n      firstName\n      lastName\n    }\n  }\n": types.UpdateProfileDocument,
    "\n  query FunctionalAreas {\n    functionalAreas\n  }\n": types.FunctionalAreasDocument,
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
export function graphql(source: "\n  query IdentityMemberships($identityToken: String!) {\n    identityMemberships(identityToken: $identityToken) {\n      tenant\n      roles\n    }\n  }\n"): typeof import('./graphql').IdentityMembershipsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n"): typeof import('./graphql').RefreshDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  fragment TenantFields on Tenant {\n    token\n    name\n    description\n    branding {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    brandingOverride {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    basemap {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    basemapOverride {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n  }\n"): typeof import('./graphql').TenantFieldsFragmentDoc;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CurrentTenant {\n    tenant {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').CurrentTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantBranding($input: TenantBrandingInput!) {\n    setTenantBranding(input: $input) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantBrandingDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantLogo($logo: String) {\n    setTenantLogo(logo: $logo) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantLogoDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantBasemap($input: TenantBasemapInput!) {\n    setTenantBasemap(input: $input) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantBasemapDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Me {\n    me {\n      email\n      firstName\n      lastName\n    }\n  }\n"): typeof import('./graphql').MeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateProfile($firstName: String, $lastName: String) {\n    updateProfile(firstName: $firstName, lastName: $lastName) {\n      email\n      firstName\n      lastName\n    }\n  }\n"): typeof import('./graphql').UpdateProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query FunctionalAreas {\n    functionalAreas\n  }\n"): typeof import('./graphql').FunctionalAreasDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
