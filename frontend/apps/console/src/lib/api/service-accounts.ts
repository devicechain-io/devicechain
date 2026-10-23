// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's service accounts, on the tenant plane of user-management: non-human principals
// an integration uses to call the APIs, with the OAuth client_credentials grant. Reading needs
// member:read and changing member:write; a member limited to part of the tenant is refused.
//
// 🔴 A CLIENT SECRET is in exactly two answers here — createServiceAccount and
// rotateServiceAccountSecret — and the console shows it once, from component state, and never
// stores or logs it. No read returns it: the server keeps only a digest.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management';
import type {
  ServiceAccountsQuery,
  CreateServiceAccountMutation,
} from '@/gql/user-management/graphql';
import type { ScopeAnchorInput } from './members';

export type ServiceAccount = ServiceAccountsQuery['serviceAccounts']['results'][number];
export type ServiceAccountPage = ServiceAccountsQuery['serviceAccounts'];
export type ServiceAccountCredentials = CreateServiceAccountMutation['createServiceAccount'];

const SERVICE_ACCOUNTS = graphql(`
  query ServiceAccounts($criteria: ServiceAccountSearchCriteria!) {
    serviceAccounts(criteria: $criteria) {
      results {
        token
        name
        description
        clientId
        enabled
        createdBy
        createdAt
        roles {
          token
          name
          owner
        }
        scope {
          kind
          token
        }
        secrets {
          hint
          createdAt
          expiresAt
          lastUsedAt
          active
        }
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
    serviceAccountTokenEndpoint
  }
`);

/** One page of the tenant's service accounts, and the token endpoint they use (null when the instance has none). */
export async function listServiceAccounts(criteria: {
  pageNumber: number;
  pageSize: number;
}): Promise<{ page: ServiceAccountPage; tokenEndpoint: string | null }> {
  const data = await gql('user-management', SERVICE_ACCOUNTS, { criteria });
  return { page: data.serviceAccounts, tokenEndpoint: data.serviceAccountTokenEndpoint ?? null };
}

const CREATE_SERVICE_ACCOUNT = graphql(`
  mutation CreateServiceAccount($request: ServiceAccountCreateRequest!) {
    createServiceAccount(request: $request) {
      clientId
      clientSecret
      tokenEndpoint
      serviceAccount {
        token
      }
    }
  }
`);

/** Create an account with its first secret. The answer's clientSecret is the only copy there will ever be. */
export async function createServiceAccount(request: {
  token: string;
  name?: string;
  description?: string;
  roleTokens: string[];
  scope: ScopeAnchorInput[];
  secretExpiresInDays?: number;
}): Promise<ServiceAccountCredentials> {
  const data = await gql('user-management', CREATE_SERVICE_ACCOUNT, { request });
  return data.createServiceAccount;
}

const SET_SERVICE_ACCOUNT_ACCESS = graphql(`
  mutation SetServiceAccountAccess($token: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {
    setServiceAccountAccess(token: $token, roleTokens: $roleTokens, scope: $scope) {
      token
    }
  }
`);

/** Replace an account's roles and scope together. */
export async function setServiceAccountAccess(token: string, roleTokens: string[], scope: ScopeAnchorInput[]): Promise<void> {
  await gql('user-management', SET_SERVICE_ACCOUNT_ACCESS, { token, roleTokens, scope });
}

const SET_SERVICE_ACCOUNT_ENABLED = graphql(`
  mutation SetServiceAccountEnabled($token: String!, $enabled: Boolean!) {
    setServiceAccountEnabled(token: $token, enabled: $enabled) {
      token
    }
  }
`);

export async function setServiceAccountEnabled(token: string, enabled: boolean): Promise<void> {
  await gql('user-management', SET_SERVICE_ACCOUNT_ENABLED, { token, enabled });
}

const ROTATE_SERVICE_ACCOUNT_SECRET = graphql(`
  mutation RotateServiceAccountSecret($token: String!, $overlapHours: Int, $expiresInDays: Int) {
    rotateServiceAccountSecret(token: $token, overlapHours: $overlapHours, expiresInDays: $expiresInDays) {
      clientId
      clientSecret
      tokenEndpoint
      serviceAccount {
        token
      }
    }
  }
`);

/** Mint a new secret; the one it replaces keeps working for overlapHours. */
export async function rotateServiceAccountSecret(
  token: string,
  overlapHours: number,
  expiresInDays?: number,
): Promise<ServiceAccountCredentials> {
  const data = await gql('user-management', ROTATE_SERVICE_ACCOUNT_SECRET, { token, overlapHours, expiresInDays });
  return data.rotateServiceAccountSecret;
}

const DELETE_SERVICE_ACCOUNT = graphql(`
  mutation DeleteServiceAccount($token: String!) {
    deleteServiceAccount(token: $token)
  }
`);

export async function deleteServiceAccount(token: string): Promise<boolean> {
  const data = await gql('user-management', DELETE_SERVICE_ACCOUNT, { token });
  return data.deleteServiceAccount;
}
