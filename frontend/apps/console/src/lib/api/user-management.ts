// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the user-management service (ADR-008 RBAC).
import { gql, resolveAuthToken } from '@devicechain/client';
import { graphql } from '@/gql/user-management';
import type {
  LoginMutation,
  SelectTenantMutation,
  CurrentTenantQuery,
  MeQuery,
} from '@/gql/user-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type IdentityAuth = LoginMutation['login'];
export type Membership = IdentityAuth['memberships'][number];
export type AuthToken = SelectTenantMutation['selectTenant'];
export type CurrentTenant = CurrentTenantQuery['tenant'];
export type CurrentUser = MeQuery['me'];

// ── Auth (unauthenticated) ──────────────────────────────────────────────
//
// Login is two-step (ADR-033): email/password authenticates the global identity
// and returns an instance-scoped identity token + the tenants it may act in; the
// caller picks one via selectTenant to get the tenant-scoped session pair.

const LOGIN = graphql(`
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
`);

export async function login(email: string, password: string): Promise<IdentityAuth> {
  const data = await gql('user-management', LOGIN, { email, password }, { anonymous: true });
  return data.login;
}

const SELECT_TENANT = graphql(`
  mutation SelectTenant($identityToken: String!, $tenant: String!) {
    selectTenant(identityToken: $identityToken, tenant: $tenant) {
      accessToken
      refreshToken
      expiresAt
    }
  }
`);

export async function selectTenant(identityToken: string, tenant: string): Promise<AuthToken> {
  const data = await gql(
    'user-management',
    SELECT_TENANT,
    { identityToken, tenant },
    { anonymous: true },
  );
  return data.selectTenant;
}

const IDENTITY_MEMBERSHIPS = graphql(`
  query IdentityMemberships($identityToken: String!) {
    identityMemberships(identityToken: $identityToken) {
      tenant
      roles
    }
  }
`);

// getIdentityMemberships re-reads the caller's live memberships from a valid
// identity token so the tenant picker reflects a mid-session membership change
// without a re-login. Runs anonymously — the token is validated as an argument.
export async function getIdentityMemberships(identityToken: string): Promise<Membership[]> {
  const data = await gql(
    'user-management',
    IDENTITY_MEMBERSHIPS,
    { identityToken },
    { anonymous: true },
  );
  return data.identityMemberships;
}

const REFRESH = graphql(`
  mutation Refresh($refreshToken: String!) {
    refresh(refreshToken: $refreshToken) {
      accessToken
      refreshToken
      expiresAt
    }
  }
`);

export async function refresh(refreshToken: string): Promise<AuthToken> {
  const data = await gql(
    'user-management',
    REFRESH,
    { refreshToken },
    { anonymous: true },
  );
  return data.refresh;
}

// ── Current tenant (authenticated) ──────────────────────────────────────
//
// Describes the tenant the caller is acting within — resolved server-side from
// the access token, so it takes no arguments. Backs the console's tenant header
// (name + token); the shape will grow to carry branding.

// The query and every tenant mutation select the SAME Tenant shape through this one
// fragment, so a mutation result can be written straight into the tenant cache
// (ADR-038 §1.2) and no surface can drift from the others.
//
// 🔴 It is a FRAGMENT rather than four hand-copied selection sets, and that is a
// direct lesson from #704: the console was deleting fields on save precisely because
// the same selection existed in several places and a new field landed in some of
// them. A field added here reaches the query and all three mutations at once, or it
// reaches none of them.
// Declared as a bare call rather than bound to a name: codegen finds it by scanning
// for graphql() templates, and every document below refers to it by its GraphQL name,
// so a binding would only be an unused local.
graphql(`
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
`);

const CURRENT_TENANT = graphql(`
  query CurrentTenant {
    tenant {
      ...TenantFields
    }
  }
`);

export async function getCurrentTenant(): Promise<CurrentTenant> {
  const data = await gql('user-management', CURRENT_TENANT);
  return data.tenant;
}

// The resolved white-labeling applied to the console shell (ADR-038). A null field
// means "inherit the built-in look" for that aspect.
export type TenantBranding = CurrentTenant['branding'];

// Self-service white-labeling of the caller's OWN tenant (requires branding:write).
// A null field CLEARS that override, re-inheriting the operator/code default.
// Returns the tenant with freshly-resolved branding for an immediate cache write.
const SET_TENANT_BRANDING = graphql(`
  mutation SetTenantBranding($input: TenantBrandingInput!) {
    setTenantBranding(input: $input) {
      ...TenantFields
    }
  }
`);

// A branding THEME override to submit: every field optional; null clears that
// override. The logo is managed separately (setTenantLogo / uploadTenantLogo) — a
// client cannot round-trip an object-store logo reference through this full replace,
// so keeping logo here would wipe an uploaded logo on a theme save (ADR-058).
export interface TenantBrandingInput {
  title?: string | null;
  logoMaxHeight?: number | null;
  primary?: string | null;
  background?: string | null;
  foreground?: string | null;
  accent?: string | null;
}

export async function setTenantBranding(input: TenantBrandingInput): Promise<CurrentTenant> {
  const data = await gql('user-management', SET_TENANT_BRANDING, {
    input: {
      title: input.title ?? null,
      logoMaxHeight: input.logoMaxHeight ?? null,
      primary: input.primary ?? null,
      background: input.background ?? null,
      foreground: input.foreground ?? null,
      accent: input.accent ?? null,
    },
  });
  return data.setTenantBranding;
}

// Set the tenant logo to an https URL (or clear it with null) — the Tier-0 path.
// A binary upload goes through uploadTenantLogo instead. Both return the tenant with
// freshly-resolved branding so the caller can write it straight into cache.
const SET_TENANT_LOGO = graphql(`
  mutation SetTenantLogo($logo: String) {
    setTenantLogo(logo: $logo) {
      ...TenantFields
    }
  }
`);

export async function setTenantLogo(logo: string | null): Promise<CurrentTenant> {
  const data = await gql('user-management', SET_TENANT_LOGO, { logo });
  return data.setTenantLogo;
}

// The tenant's EFFECTIVE basemap: its own override folded over the operator default.
// The instance default ships a real tile source, so this normally arrives set — but
// every field may still be null, because an operator can clear it, and the surfaces
// then draw a plain panel.
export type TenantBasemap = CurrentTenant['basemap'];

// Self-service basemap for the caller's OWN tenant (requires basemap:write — NOT
// branding:write, because the tile URL carries the tenant's provider credential).
// Returns the tenant with a freshly-resolved basemap for an immediate cache write.
const SET_TENANT_BASEMAP = graphql(`
  mutation SetTenantBasemap($input: TenantBasemapInput!) {
    setTenantBasemap(input: $input) {
      ...TenantFields
    }
  }
`);

// A basemap override to submit. Every field is optional and this is a FULL REPLACE:
// an omitted field clears that override.
//
// 🔴 `Required<>` on the argument of setTenantBasemap below is the #704 fix applied
// up front: it makes each field mandatory AND strips `undefined`, so a caller that
// forgets one does not compile rather than silently clearing it. Pass an explicit
// null to clear.
export interface TenantBasemapInput {
  tileUrl: string | null;
  attribution: string | null;
  centerLat: number | null;
  centerLon: number | null;
  zoom: number | null;
}

export async function setTenantBasemap(
  input: Required<TenantBasemapInput>,
): Promise<CurrentTenant> {
  const data = await gql('user-management', SET_TENANT_BASEMAP, { input });
  return data.setTenantBasemap;
}

// The tenant's DEFAULT language: its own override folded over the operator's
// `locale.default`. A BCP-47 tag, or null when neither tier sets one.
//
// 🔴 A DEFAULT, not the language in effect. It is rung 2 of four — an explicit user
// choice beats it, it beats the browser's advertised languages, and English catches
// the rest — and the only thing that applies it is applyTenantDefaultLocale, called
// once from TenantProvider. Reading this value anywhere else to decide what language
// something is in would skip rung 1 and re-language a user who has chosen.
export type TenantLocale = CurrentTenant['locale'];

// Self-service default language for the caller's OWN tenant (requires locale:write —
// NOT branding:write, because this re-languages the console for every member who has
// not chosen otherwise). Passing null clears the override, re-inheriting the operator
// default. Returns the tenant with a freshly-resolved locale for an immediate cache
// write.
const SET_TENANT_LOCALE = graphql(`
  mutation SetTenantLocale($locale: String) {
    setTenantLocale(locale: $locale) {
      ...TenantFields
    }
  }
`);

export async function setTenantLocale(locale: string | null): Promise<CurrentTenant> {
  const data = await gql('user-management', SET_TENANT_LOCALE, { locale });
  return data.setTenantLocale;
}

// uploadTenantLogo uploads a raster logo file to the object store (ADR-058 Tier-1)
// and points the tenant's branding_logo at it. It POSTs the raw bytes to the
// authorizing endpoint with the caller's access token (the server sniffs the real
// content type; the Content-Type header is advisory). The response carries the
// read-proxy path for the new logo; callers should refetch the tenant to pick up
// the freshly-resolved branding. Requires branding:write.
export async function uploadTenantLogo(file: File): Promise<{ logo: string }> {
  const token = await resolveAuthToken();
  const res = await fetch('/api/user-management/branding/logo', {
    method: 'POST',
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      'Content-Type': file.type || 'application/octet-stream',
    },
    body: file,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(text.trim() || `Logo upload failed (${res.status})`);
  }
  return (await res.json()) as { logo: string };
}

// Describes the identity the caller is signed in as — resolved server-side from
// the access token. Backs the console's user menu (name, falling back to email).

const ME = graphql(`
  query Me {
    me {
      email
      firstName
      lastName
    }
  }
`);

export async function getCurrentUser(): Promise<CurrentUser> {
  const data = await gql('user-management', ME);
  return data.me;
}

// Self-service edit of the signed-in user's display name (email is fixed).

const UPDATE_PROFILE = graphql(`
  mutation UpdateProfile($request: ProfileUpdateRequest!) {
    updateProfile(request: $request) {
      email
      firstName
      lastName
    }
  }
`);

// A partial update on a dedicated input, like every other update on the platform: an
// omitted field is left alone, and a `null` — or an empty string, which is what the form
// sends for a name the user cleared — sets it to empty.
//
// 🔴 THE `?? null` IS DELIBERATE AND IS NOT THE PARTIAL-UPDATE ESCAPE HATCH. This is the
// profile FORM's client: it renders both names, so on save it states both, and a field
// the user emptied has to be sent as something rather than omitted. Passing the request
// through would let a caller omit a key and mean "leave it alone" — correct, but it is a
// capability this one screen has no use for and the coercion here is what keeps
// `firstName: undefined` from silently becoming that.
export async function updateProfile(input: {
  firstName?: string | null;
  lastName?: string | null;
}): Promise<CurrentUser> {
  const data = await gql('user-management', UPDATE_PROFILE, {
    request: { firstName: input.firstName ?? null, lastName: input.lastName ?? null },
  });
  return data.updateProfile;
}

// ── Deployed functional areas ───────────────────────────────────────────
//
// Which areas this instance deployed, so the console can tell "this feature was
// never deployed here" apart from "the server is broken". The ingress emits a
// rule only for enabled areas, so a call to an absent one falls through to the
// SPA's static nginx and returns 405 — a status describing nginx's routing
// rather than the request. Served on the tenant plane AND the admin plane
// (listAdminFunctionalAreas) because the affected surfaces straddle both and
// neither token subsumes the other. See lib/capabilities.tsx.

const FUNCTIONAL_AREAS = graphql(`
  query FunctionalAreas {
    functionalAreas
  }
`);

export async function listFunctionalAreas(): Promise<string[]> {
  const data = await gql('user-management', FUNCTIONAL_AREAS);
  return data.functionalAreas;
}

// ── Entity token masks ──────────────────────────────────────────────────
//
// The operator's token-mask templates, which create forms use to mint a token.
// Served on the tenant plane AND the identity lane (settings.ts) for the same
// reason functionalAreas above is: the create forms straddle both, and neither
// token subsumes the other. A tenant-scoped form holds a session that lasts days
// and an identity token that dies in fifteen minutes with no refresh; an operator
// creating the first tenant holds an identity session and no tenant session at
// all. See lib/token-masks.ts, which picks the lane.

const TOKEN_MASKS = graphql(`
  query TenantTokenMasks {
    tokenMasks
  }
`);

export async function getTenantTokenMasks(): Promise<string> {
  const data = await gql('user-management', TOKEN_MASKS);
  return data.tokenMasks;
}
