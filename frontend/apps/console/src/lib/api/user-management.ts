// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the user-management service (ADR-008 RBAC).
import { gql } from '@devicechain/client';
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

const CURRENT_TENANT = graphql(`
  query CurrentTenant {
    tenant {
      token
      name
      description
    }
  }
`);

export async function getCurrentTenant(): Promise<CurrentTenant> {
  const data = await gql('user-management', CURRENT_TENANT);
  return data.tenant;
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
  mutation UpdateProfile($firstName: String, $lastName: String) {
    updateProfile(firstName: $firstName, lastName: $lastName) {
      email
      firstName
      lastName
    }
  }
`);

export async function updateProfile(input: {
  firstName?: string | null;
  lastName?: string | null;
}): Promise<CurrentUser> {
  const data = await gql('user-management', UPDATE_PROFILE, {
    firstName: input.firstName ?? null,
    lastName: input.lastName ?? null,
  });
  return data.updateProfile;
}
