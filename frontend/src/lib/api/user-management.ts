// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the user-management service (ADR-008 RBAC).
import { gql } from '@/lib/graphql/client';

export interface AuthToken {
  accessToken: string;
  refreshToken: string;
  expiresAt: string;
}

export interface Role {
  id: string;
  token: string;
  name: string | null;
  description: string | null;
  authorities: string[];
  createdAt: string | null;
  updatedAt: string | null;
}

export interface User {
  id: string;
  username: string;
  email: string | null;
  firstName: string | null;
  lastName: string | null;
  enabled: boolean;
  roles: Role[];
  createdAt: string | null;
  updatedAt: string | null;
}

// ── Auth (unauthenticated) ──────────────────────────────────────────────

const LOGIN = `
  mutation Login($username: String!, $password: String!) {
    login(username: $username, password: $password) {
      accessToken
      refreshToken
      expiresAt
    }
  }
`;

export async function login(username: string, password: string): Promise<AuthToken> {
  const data = await gql<{ login: AuthToken }>(
    'user-management',
    LOGIN,
    { username, password },
    { anonymous: true },
  );
  return data.login;
}

const REFRESH = `
  mutation Refresh($refreshToken: String!) {
    refresh(refreshToken: $refreshToken) {
      accessToken
      refreshToken
      expiresAt
    }
  }
`;

export async function refresh(refreshToken: string): Promise<AuthToken> {
  const data = await gql<{ refresh: AuthToken }>(
    'user-management',
    REFRESH,
    { refreshToken },
    { anonymous: true },
  );
  return data.refresh;
}

// ── Directory (authenticated) ───────────────────────────────────────────

const ROLE_FIELDS = `
  id
  token
  name
  description
  authorities
  createdAt
  updatedAt
`;

const USERS = `
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
      roles { ${ROLE_FIELDS} }
    }
  }
`;

export async function listUsers(): Promise<User[]> {
  const data = await gql<{ users: User[] }>('user-management', USERS);
  return data.users;
}

const ROLES = `
  query Roles {
    roles { ${ROLE_FIELDS} }
  }
`;

export async function listRoles(): Promise<Role[]> {
  const data = await gql<{ roles: Role[] }>('user-management', ROLES);
  return data.roles;
}