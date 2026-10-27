// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents for the standalone dashboard viewer.
//
// Like the SDK packages, this app carries no graphql-codegen — the SDK runs in
// documentMode 'string', so a raw query string cast to TypedDocument<Result, Vars>
// is exactly what a generated document is at runtime. The viewer only needs the
// two-step auth flow (ADR-033): login authenticates the identity and lists its
// tenants; selectTenant exchanges the identity token for a tenant access token.
// A board is loaded one of two ways: fetched by token (the published snapshot, below), or
// pasted in by an external embedder with no access to the service.

import type { TypedDocument } from '@devicechain/client';

// ── user-management: authenticate a global identity ──────────────────────────
// Anonymous (no bearer). Returns an instance-scoped identity token + the tenants
// the identity may act in; the caller picks one via selectTenant.

export interface Membership {
  tenant: string;
  roles: string[];
  /**
   * The tenant requires single sign-on and this was a password sign-in: selectTenant
   * refuses it (code SSO_REQUIRED). Listed so the viewer can say why, not offered as a choice.
   */
  ssoRequired: boolean;
}
export interface LoginResult {
  login: { identityToken: string; memberships: Membership[] };
}
export interface LoginVariables {
  email: string;
  password: string;
}

export const LOGIN = `
  mutation Login($email: String!, $password: String!) {
    login(email: $email, password: $password) {
      identityToken
      memberships {
        tenant
        roles
        ssoRequired
      }
    }
  }
` as unknown as TypedDocument<LoginResult, LoginVariables>;

// ── user-management: finish a sign-in login held for its second factor ───────
// Anonymous. An account with two-factor authentication on gets the error code MFA_CHALLENGE
// from login; the challenge handle and a code (six digits, or a recovery code) complete it
// and answer what login answers.

export interface VerifyMfaResult {
  verifyMfa: { identityToken: string; memberships: Membership[] };
}
export interface VerifyMfaVariables {
  challenge: string;
  code: string;
}
export const VERIFY_MFA = `
  mutation VerifyMfa($challenge: String!, $code: String!) {
    verifyMfa(challenge: $challenge, code: $code) {
      identityToken
      memberships {
        tenant
        roles
        ssoRequired
      }
    }
  }
` as unknown as TypedDocument<VerifyMfaResult, VerifyMfaVariables>;

// ── user-management: exchange the identity token for a tenant session ────────
// Anonymous (the identity token is validated as an argument). Returns the
// tenant-scoped access token the viewer attaches to every subsequent request.

export interface SelectTenantResult {
  selectTenant: { accessToken: string; refreshToken: string };
}
export interface SelectTenantVariables {
  identityToken: string;
  tenant: string;
}

export const SELECT_TENANT = `
  mutation SelectTenant($identityToken: String!, $tenant: String!) {
    selectTenant(identityToken: $identityToken, tenant: $tenant) {
      accessToken
      refreshToken
    }
  }
` as unknown as TypedDocument<SelectTenantResult, SelectTenantVariables>;

// ── user-management: refresh the tenant session ──────────────────────────────
// Anonymous (the refresh token is the argument). Exchanges the refresh token for a new
// access/refresh pair; the old refresh token is spent, so only one refresh may be in
// flight (createSessionTokens guarantees that). Called ahead of the access token's
// expiry so a wall display never lapses.

export interface RefreshResult {
  refresh: { accessToken: string; refreshToken: string };
}
export interface RefreshVariables {
  refreshToken: string;
}

export const REFRESH = `
  mutation Refresh($refreshToken: String!) {
    refresh(refreshToken: $refreshToken) {
      accessToken
      refreshToken
    }
  }
` as unknown as TypedDocument<RefreshResult, RefreshVariables>;

// ── user-management: single sign-on ──────────────────────────────────────────
// Anonymous. beginSso answers with the identity provider's address (null when no
// connection speaks for the email's domain); completeSso redeems the one-time code the
// provider's return carried and answers like login does, so selectTenant follows.

export interface BeginSsoResult {
  beginSso: { authorizationUrl: string } | null;
}
export interface BeginSsoVariables {
  email: string;
  returnTo: string;
  codeChallenge: string;
}

export const BEGIN_SSO = `
  mutation BeginSso($email: String, $returnTo: String!, $codeChallenge: String!) {
    beginSso(email: $email, returnTo: $returnTo, codeChallenge: $codeChallenge) {
      authorizationUrl
    }
  }
` as unknown as TypedDocument<BeginSsoResult, BeginSsoVariables>;

export interface CompleteSsoResult {
  completeSso: { identityToken: string; memberships: Membership[] };
}
export interface CompleteSsoVariables {
  code: string;
  codeVerifier: string;
}

export const COMPLETE_SSO = `
  mutation CompleteSso($code: String!, $codeVerifier: String!) {
    completeSso(code: $code, codeVerifier: $codeVerifier) {
      identityToken
      memberships {
        tenant
        roles
        ssoRequired
      }
    }
  }
` as unknown as TypedDocument<CompleteSsoResult, CompleteSsoVariables>;

// ── user-management: end the sign-in ─────────────────────────────────────────
// Anonymous (the token is the argument). Ends the sign-in server-side, so the refresh
// token this viewer was handed stops working instead of living out its lifetime. The
// viewer never uses the refresh token itself - it exists only to be handed back here.
// It names this app as the one to come back to, so a sign-in through a connection set to
// sign people out at the identity provider too is answered with the provider's sign-out
// address; the server builds the address the provider returns to, never this viewer.

export interface LogoutResult {
  logout: { providerSignOutUrl: string | null } | null;
}
export interface LogoutVariables {
  token: string;
}

export const LOGOUT = `
  mutation Logout($token: String!) {
    logout(token: $token, returnTo: DASHBOARD) {
      providerSignOutUrl
    }
  }
` as unknown as TypedDocument<LogoutResult, LogoutVariables>;

// ── user-management: the tenant's effective basemap ──────────────────────────
// Authenticated (rides the access token), self-scoped, and requires no authority —
// the tenant query resolves whichever tenant the token names.
//
// 🔴 This viewer needs it for the same reason the console does, and it is the surface
// most likely to be forgotten: it has its OWN login and its own render tree, so a
// basemap wired only into the console produces "works in the console, blank when
// embedded" — reported by whoever embedded the board, not by whoever configured it.

export interface TenantBasemapResult {
  tenant: {
    basemap: {
      tileUrl: string | null;
      attribution: string | null;
      centerLat: number | null;
      centerLon: number | null;
      zoom: number | null;
    };
  };
}

export const TENANT_BASEMAP = `
  query TenantBasemap {
    tenant {
      basemap {
        tileUrl
        attribution
        centerLat
        centerLon
        zoom
      }
    }
  }
` as unknown as TypedDocument<TenantBasemapResult, Record<string, never>>;

// ── dashboard-management: the published snapshot of a dashboard ──────────────
// Authenticated (rides the tenant access token) and gated on dashboard:read. This is
// the snapshot viewers are served -- never the draft, which is author-only. Null when
// no such dashboard exists in the tenant; a NOT_PUBLISHED error when it exists but was
// never published.

export interface PublishedDashboardResult {
  publishedDashboard: { definition: string } | null;
}
export interface PublishedDashboardVariables {
  token: string;
}

export const PUBLISHED_DASHBOARD = `
  query PublishedDashboard($token: String!) {
    publishedDashboard(token: $token) {
      definition
    }
  }
` as unknown as TypedDocument<PublishedDashboardResult, PublishedDashboardVariables>;

// ── dashboard-management: the dashboards this viewer may open ────────────────
// One page of the tenant's dashboards, metadata only. For a member limited to part of the
// tenant (a customer user) the server answers only the PUBLISHED dashboards shared with
// their customers; for anyone else, every dashboard, and the viewer keeps the published
// ones, since a never-published dashboard has nothing it can show.

export interface DashboardListItem {
  token: string;
  name: string | null;
  description: string | null;
  publishedVersion: number | null;
}
export interface DashboardsResult {
  dashboards: { results: DashboardListItem[]; pagination: { totalRecords: number | null } };
}
export interface DashboardsVariables {
  criteria: { pageNumber: number; pageSize: number };
}

export const DASHBOARDS = `
  query Dashboards($criteria: DashboardSearchCriteria!) {
    dashboards(criteria: $criteria) {
      results {
        token
        name
        description
        publishedVersion
      }
      pagination {
        totalRecords
      }
    }
  }
` as unknown as TypedDocument<DashboardsResult, DashboardsVariables>;
