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
      }
    }
  }
` as unknown as TypedDocument<LoginResult, LoginVariables>;

// ── user-management: exchange the identity token for a tenant session ────────
// Anonymous (the identity token is validated as an argument). Returns the
// tenant-scoped access token the viewer attaches to every subsequent request.

export interface SelectTenantResult {
  selectTenant: { accessToken: string };
}
export interface SelectTenantVariables {
  identityToken: string;
  tenant: string;
}

export const SELECT_TENANT = `
  mutation SelectTenant($identityToken: String!, $tenant: String!) {
    selectTenant(identityToken: $identityToken, tenant: $tenant) {
      accessToken
    }
  }
` as unknown as TypedDocument<SelectTenantResult, SelectTenantVariables>;

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
