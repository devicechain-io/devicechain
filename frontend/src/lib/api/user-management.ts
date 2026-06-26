// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the user-management service (ADR-008 RBAC).
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/user-management';
import type {
  LoginMutation,
  UsersQuery,
  RolesQuery,
} from '@/gql/user-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type AuthToken = LoginMutation['login'];
export type User = UsersQuery['users'][number];
export type Role = RolesQuery['roles'][number];

// ── Auth (unauthenticated) ──────────────────────────────────────────────

const LOGIN = graphql(`
  mutation Login($username: String!, $password: String!) {
    login(username: $username, password: $password) {
      accessToken
      refreshToken
      expiresAt
    }
  }
`);

export async function login(username: string, password: string): Promise<AuthToken> {
  const data = await gql(
    'user-management',
    LOGIN,
    { username, password },
    { anonymous: true },
  );
  return data.login;
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

// ── Directory (authenticated) ───────────────────────────────────────────

const USERS = graphql(`
  query Users {
    users {
      id
      username
      email
      firstName
      lastName
      enabled
      createdAt
      updatedAt
      roles {
        id
        token
        name
        description
        authorities
        createdAt
        updatedAt
      }
    }
  }
`);

export async function listUsers(): Promise<User[]> {
  const data = await gql('user-management', USERS);
  return data.users;
}

const ROLES = graphql(`
  query Roles {
    roles {
      id
      token
      name
      description
      authorities
      createdAt
      updatedAt
    }
  }
`);

export async function listRoles(): Promise<Role[]> {
  const data = await gql('user-management', ROLES);
  return data.roles;
}
