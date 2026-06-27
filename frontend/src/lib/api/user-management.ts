// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the user-management service (ADR-008 RBAC).
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/user-management';
import type {
  LoginMutation,
  SelectTenantMutation,
} from '@/gql/user-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type IdentityAuth = LoginMutation['login'];
export type Membership = IdentityAuth['memberships'][number];
export type AuthToken = SelectTenantMutation['selectTenant'];

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
