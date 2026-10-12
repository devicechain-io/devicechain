// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for the instance-scoped single-sign-on administration
// surface, served by user-management on the same identity-token lane as the rest of
// the admin API (see admin.ts). A connection is an external OpenID Connect provider
// people sign in through. Its client secret is WRITE-ONLY: it goes in on create and
// through setIdpConnectionSecret, and the read side only ever says whether one is
// stored (`hasSecret`).
//
// Mutations select only `token`. The page re-reads the connection after every write,
// so the screen always renders what the server holds rather than what a mutation
// payload happened to carry.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management-admin';
import type {
  IdpConnectionsQuery,
  IdpConnectionQuery,
  SsoLinkRequestsQuery,
  IdpConnectionCreateRequest,
} from '@/gql/user-management-admin/graphql';

export type IdpConnectionListItem = IdpConnectionsQuery['idpConnections'][number];
export type IdpConnection = NonNullable<IdpConnectionQuery['idpConnection']>;
export type IdpDomain = IdpConnection['domains'][number];
export type IdpDiscovery = IdpConnection['discovery'];
export type SsoLinkRequest = SsoLinkRequestsQuery['ssoLinkRequests'][number];
export type { IdpConnectionCreateRequest };

export interface IdpClaim {
  name: string;
  value: string;
}

const opts = { identity: true } as const;

// ── Queries ─────────────────────────────────────────────────────────────

const IDP_CONNECTIONS = graphql(`
  query IdpConnections {
    idpConnections {
      token
      name
      kind
      enabled
      issuer
      hasSecret
      tenants
      domains {
        domain
      }
      discovery {
        approved
        fingerprint
      }
    }
  }
`);

export async function listIdpConnections(): Promise<IdpConnectionListItem[]> {
  const data = await gql('user-management/admin', IDP_CONNECTIONS, undefined, opts);
  return data.idpConnections;
}

const IDP_CONNECTION = graphql(`
  query IdpConnection($token: String!) {
    idpConnection(token: $token) {
      token
      name
      kind
      enabled
      issuer
      clientId
      hasSecret
      scopes
      emailClaim
      requiredClaims {
        name
        value
      }
      requiredAmr
      domains {
        domain
        verifiedAt
        verifiedBy
      }
      tenants
      callbackUrl
      providerSignOut
      signOutReturnUrls
      discovery {
        authorizationEndpoint
        tokenEndpoint
        jwksUri
        endSessionEndpoint
        fetchedAt
        fingerprint
        approved
        approvedBy
        approvedAt
      }
      createdAt
      updatedAt
    }
  }
`);

export async function getIdpConnection(token: string): Promise<IdpConnection | null> {
  const data = await gql('user-management/admin', IDP_CONNECTION, { token }, opts);
  return data.idpConnection ?? null;
}

const SSO_LINK_REQUESTS = graphql(`
  query SsoLinkRequests {
    ssoLinkRequests {
      id
      connection
      subject
      email
      createdAt
      identityExists
      subjectChanged
      manualMemberships
    }
  }
`);

export async function listSsoLinkRequests(): Promise<SsoLinkRequest[]> {
  const data = await gql('user-management/admin', SSO_LINK_REQUESTS, undefined, opts);
  return data.ssoLinkRequests;
}

// ── Mutations ───────────────────────────────────────────────────────────

const CREATE_IDP_CONNECTION = graphql(`
  mutation CreateIdpConnection($request: IdpConnectionCreateRequest!) {
    createIdpConnection(request: $request) {
      token
    }
  }
`);

export async function createIdpConnection(
  request: IdpConnectionCreateRequest,
): Promise<{ token: string }> {
  const data = await gql('user-management/admin', CREATE_IDP_CONNECTION, { request }, opts);
  return data.createIdpConnection;
}

// updateIdpConnection is a PARTIAL update: a key left out keeps the stored value. The
// object is forwarded as-is so a field the caller never set stays absent on the wire.
const UPDATE_IDP_CONNECTION = graphql(`
  mutation UpdateIdpConnection($token: String!, $request: IdpConnectionUpdateRequest!) {
    updateIdpConnection(token: $token, request: $request) {
      token
    }
  }
`);

export interface IdpConnectionChanges {
  name?: string;
  enabled?: boolean;
  clientId?: string;
  emailClaim?: string;
  scopes?: string[];
  requiredAmr?: string[];
  providerSignOut?: boolean;
}

export async function updateIdpConnection(
  token: string,
  request: IdpConnectionChanges,
): Promise<{ token: string }> {
  const data = await gql('user-management/admin', UPDATE_IDP_CONNECTION, { token, request }, opts);
  return data.updateIdpConnection;
}

const SET_IDP_REQUIRED_CLAIMS = graphql(`
  mutation SetIdpRequiredClaims($token: String!, $claims: [IdpClaimRequirementInput!]!) {
    setIdpRequiredClaims(token: $token, claims: $claims) {
      token
    }
  }
`);

export async function setIdpRequiredClaims(token: string, claims: IdpClaim[]): Promise<void> {
  await gql('user-management/admin', SET_IDP_REQUIRED_CLAIMS, { token, claims }, opts);
}

const DELETE_IDP_CONNECTION = graphql(`
  mutation DeleteIdpConnection($token: String!) {
    deleteIdpConnection(token: $token)
  }
`);

export async function deleteIdpConnection(token: string): Promise<boolean> {
  const data = await gql('user-management/admin', DELETE_IDP_CONNECTION, { token }, opts);
  return data.deleteIdpConnection;
}

const SET_IDP_CONNECTION_SECRET = graphql(`
  mutation SetIdpConnectionSecret($token: String!, $secret: String!) {
    setIdpConnectionSecret(token: $token, secret: $secret) {
      token
    }
  }
`);

export async function setIdpConnectionSecret(token: string, secret: string): Promise<void> {
  await gql('user-management/admin', SET_IDP_CONNECTION_SECRET, { token, secret }, opts);
}

const SET_IDP_CONNECTION_TENANTS = graphql(`
  mutation SetIdpConnectionTenants($token: String!, $tenants: [String!]!) {
    setIdpConnectionTenants(token: $token, tenants: $tenants) {
      token
    }
  }
`);

export async function setIdpConnectionTenants(token: string, tenants: string[]): Promise<void> {
  await gql('user-management/admin', SET_IDP_CONNECTION_TENANTS, { token, tenants }, opts);
}

const ADD_IDP_DOMAIN = graphql(`
  mutation AddIdpDomain($token: String!, $domain: String!) {
    addIdpDomain(token: $token, domain: $domain) {
      token
    }
  }
`);

export async function addIdpDomain(token: string, domain: string): Promise<void> {
  await gql('user-management/admin', ADD_IDP_DOMAIN, { token, domain }, opts);
}

const REMOVE_IDP_DOMAIN = graphql(`
  mutation RemoveIdpDomain($token: String!, $domain: String!) {
    removeIdpDomain(token: $token, domain: $domain) {
      token
    }
  }
`);

export async function removeIdpDomain(token: string, domain: string): Promise<void> {
  await gql('user-management/admin', REMOVE_IDP_DOMAIN, { token, domain }, opts);
}

const REFRESH_IDP_DISCOVERY = graphql(`
  mutation RefreshIdpDiscovery($token: String!) {
    refreshIdpDiscovery(token: $token) {
      token
    }
  }
`);

export async function refreshIdpDiscovery(token: string): Promise<void> {
  await gql('user-management/admin', REFRESH_IDP_DISCOVERY, { token }, opts);
}

const APPROVE_IDP_DISCOVERY = graphql(`
  mutation ApproveIdpDiscovery($token: String!, $fingerprint: String!) {
    approveIdpDiscovery(token: $token, fingerprint: $fingerprint) {
      token
    }
  }
`);

// The fingerprint names the snapshot the operator reviewed; the server refuses when the
// stored snapshot is no longer that one.
export async function approveIdpDiscovery(token: string, fingerprint: string): Promise<void> {
  await gql('user-management/admin', APPROVE_IDP_DISCOVERY, { token, fingerprint }, opts);
}

const APPROVE_SSO_LINK_REQUEST = graphql(`
  mutation ApproveSsoLinkRequest($id: ID!, $membershipDisposition: String) {
    approveSsoLinkRequest(id: $id, membershipDisposition: $membershipDisposition)
  }
`);

export async function approveSsoLinkRequest(
  id: string,
  membershipDisposition?: 'convert' | 'keep_manual',
): Promise<boolean> {
  const data = await gql(
    'user-management/admin',
    APPROVE_SSO_LINK_REQUEST,
    { id, membershipDisposition: membershipDisposition ?? null },
    opts,
  );
  return data.approveSsoLinkRequest;
}

const REJECT_SSO_LINK_REQUEST = graphql(`
  mutation RejectSsoLinkRequest($id: ID!) {
    rejectSsoLinkRequest(id: $id)
  }
`);

export async function rejectSsoLinkRequest(id: string): Promise<boolean> {
  const data = await gql('user-management/admin', REJECT_SSO_LINK_REQUEST, { id }, opts);
  return data.rejectSsoLinkRequest;
}
