// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents for the standalone dashboard viewer.
//
// Like the SDK packages, this app carries no graphql-codegen — the SDK runs in
// documentMode 'string', so a raw query string cast to TypedDocument<Result, Vars>
// is exactly what a generated document is at runtime. The viewer only needs the
// two-step auth flow (ADR-033): login authenticates the identity and lists its
// tenants; selectTenant exchanges the identity token for a tenant access token.
// The definition itself is pasted in, not fetched — so there is no dashboard query.

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
