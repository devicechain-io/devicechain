// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's own administration of its members, invitations and roles, on the tenant
// plane of user-management. Everything here acts on the caller's own tenant (named by the
// access token), needs member:read/member:write or role:read/role:write, and is refused to
// a member whose access is limited to part of the tenant.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management';
import type {
  MembersQuery,
  InvitationsQuery,
  TenantRolesQuery,
  InviteMemberMutation,
  ScopeAnchorKind,
} from '@/gql/user-management/graphql';

export type Member = MembersQuery['members']['results'][number];
export type MemberPage = MembersQuery['members'];
export type Invitation = InvitationsQuery['invitations']['results'][number];
export type TenantRole = TenantRolesQuery['tenantRoles'][number];
export type MemberInvitation = InviteMemberMutation['inviteMember'];

/** One entry of a scope as the API takes it: a customer, or a group that is an access scope. */
export interface ScopeAnchorInput {
  kind: ScopeAnchorKind;
  token: string;
}

/** A customer anchor for the given customer token. */
export function customerAnchor(token: string): ScopeAnchorInput {
  return { kind: 'CUSTOMER' as ScopeAnchorKind, token };
}

/** A group anchor for the given group token. */
export function groupAnchor(token: string): ScopeAnchorInput {
  return { kind: 'GROUP' as ScopeAnchorKind, token };
}

// One selection for a member everywhere, so a list row, a detail read and a write's
// answer are the same shape and a write can replace the row it changed in place.
const MEMBERS = graphql(`
  query Members($criteria: MemberSearchCriteria!) {
    members(criteria: $criteria) {
      results {
        email
        firstName
        lastName
        enabled
        roles {
          token
          name
          owner
        }
        scope {
          kind
          token
        }
        managedByIdentityProvider
        invitationPending
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

/** The cuts of the member list the server makes; they combine with AND. */
export interface MemberFilter {
  emailPrefix?: string;
  enabled?: boolean;
  /** Only members holding the role with this token. */
  roleToken?: string;
  /** Only members whose scope names this customer or group. */
  scopeAnchor?: ScopeAnchorInput;
  /** True for memberships the identity provider manages, false for manual ones. */
  managedByIdentityProvider?: boolean;
}

export async function listMembers(
  criteria: MemberFilter & {
    pageNumber: number;
    pageSize: number;
  },
): Promise<MemberPage> {
  const data = await gql('user-management', MEMBERS, { criteria });
  return data.members;
}

// The most pages listAllMembers will read. The server caps a page at 1000, so this is ten
// thousand members. The server makes the cut, so a view asking for one customer's users
// reads only those. Reaching the cap is reported (truncated), never answered with a list
// that silently stops part-way and never thrown: the view shows what it has and says to
// narrow the cut.
const MAX_MEMBER_PAGES = 10;

/** The members matching a filter, and whether more matched than were read. */
export interface AllMembers {
  members: Member[];
  truncated: boolean;
}

/** Every member matching the filter, read page by page up to the cap. */
export async function listAllMembers(filter: MemberFilter = {}): Promise<AllMembers> {
  const pageSize = 1000;
  const out: Member[] = [];
  for (let page = 1; page <= MAX_MEMBER_PAGES; page++) {
    const res = await listMembers({ ...filter, pageNumber: page, pageSize });
    out.push(...res.results);
    const total = res.pagination.totalRecords ?? out.length;
    if (out.length >= total || res.results.length < pageSize) return { members: out, truncated: false };
  }
  return { members: out, truncated: true };
}

const INVITATIONS = graphql(`
  query Invitations($criteria: InvitationSearchCriteria!) {
    invitations(criteria: $criteria) {
      results {
        email
        state
        createdAt
        expiresAt
        acceptedAt
        revokedAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listInvitations(criteria: {
  pageNumber: number;
  pageSize: number;
}): Promise<InvitationsQuery['invitations']> {
  const data = await gql('user-management', INVITATIONS, { criteria });
  return data.invitations;
}

const TENANT_ROLES = graphql(`
  query TenantRoles {
    tenantRoles {
      token
      name
      description
      authorities
      owner
    }
  }
`);

export async function listTenantRoles(): Promise<TenantRole[]> {
  const data = await gql('user-management', TENANT_ROLES);
  return data.tenantRoles;
}

const TENANT_AUTHORITIES = graphql(`
  query TenantAuthorities {
    tenantAuthorities
  }
`);

/** The authorities a role defined in this tenant may grant, including `*`. */
export async function listTenantAuthorities(): Promise<string[]> {
  const data = await gql('user-management', TENANT_AUTHORITIES);
  return data.tenantAuthorities;
}

// ── Member writes ────────────────────────────────────────────────────────

const INVITE_MEMBER = graphql(`
  mutation InviteMember($request: InviteMemberRequest!) {
    inviteMember(request: $request) {
      member {
        email
      }
      invitationCode
    }
  }
`);

/**
 * Invite a person with the given roles and scope. The answer carries the one-time code;
 * it is shown to the inviting admin once and never stored or logged by the console.
 */
export async function inviteMember(request: {
  email: string;
  roleTokens: string[];
  scope: ScopeAnchorInput[];
}): Promise<MemberInvitation> {
  const data = await gql('user-management', INVITE_MEMBER, { request });
  return data.inviteMember;
}

const SET_MEMBER_ROLES = graphql(`
  mutation SetMemberRoles($email: String!, $roleTokens: [String!]!) {
    setMemberRoles(email: $email, roleTokens: $roleTokens) {
      email
    }
  }
`);

export async function setMemberRoles(email: string, roleTokens: string[]): Promise<void> {
  await gql('user-management', SET_MEMBER_ROLES, { email, roleTokens });
}

const SET_MEMBER_SCOPE = graphql(`
  mutation SetMemberScope($email: String!, $scope: [ScopeAnchorInput!]!) {
    setMemberScope(email: $email, scope: $scope) {
      email
    }
  }
`);

export async function setMemberScope(email: string, scope: ScopeAnchorInput[]): Promise<void> {
  await gql('user-management', SET_MEMBER_SCOPE, { email, scope });
}

const SET_MEMBER_ACCESS = graphql(`
  mutation SetMemberAccess($email: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {
    setMemberAccess(email: $email, roleTokens: $roleTokens, scope: $scope) {
      email
    }
  }
`);

/** Replace a member's roles and scope together: both change or neither does. */
export async function setMemberAccess(email: string, roleTokens: string[], scope: ScopeAnchorInput[]): Promise<void> {
  await gql('user-management', SET_MEMBER_ACCESS, { email, roleTokens, scope });
}

const SET_MEMBER_ENABLED = graphql(`
  mutation SetMemberEnabled($email: String!, $enabled: Boolean!) {
    setMemberEnabled(email: $email, enabled: $enabled) {
      email
    }
  }
`);

export async function setMemberEnabled(email: string, enabled: boolean): Promise<void> {
  await gql('user-management', SET_MEMBER_ENABLED, { email, enabled });
}

const REMOVE_MEMBER = graphql(`
  mutation RemoveMember($email: String!) {
    removeMember(email: $email)
  }
`);

export async function removeMember(email: string): Promise<void> {
  await gql('user-management', REMOVE_MEMBER, { email });
}

const REVOKE_INVITATION = graphql(`
  mutation RevokeInvitation($email: String!) {
    revokeInvitation(email: $email) {
      email
    }
  }
`);

export async function revokeInvitation(email: string): Promise<void> {
  await gql('user-management', REVOKE_INVITATION, { email });
}

const REISSUE_INVITATION = graphql(`
  mutation ReissueInvitation($email: String!) {
    reissueInvitation(email: $email) {
      member {
        email
      }
      invitationCode
    }
  }
`);

export async function reissueInvitation(email: string): Promise<MemberInvitation> {
  const data = await gql('user-management', REISSUE_INVITATION, { email });
  return data.reissueInvitation;
}

// ── Tenant roles ─────────────────────────────────────────────────────────

const CREATE_TENANT_ROLE = graphql(`
  mutation CreateTenantRole($request: TenantRoleCreateRequest!) {
    createTenantRole(request: $request) {
      token
    }
  }
`);

export async function createTenantRole(request: {
  token: string;
  name?: string;
  description?: string;
  authorities: string[];
}): Promise<void> {
  await gql('user-management', CREATE_TENANT_ROLE, { request });
}

const UPDATE_TENANT_ROLE = graphql(`
  mutation UpdateTenantRole($token: String!, $request: TenantRoleUpdateRequest!) {
    updateTenantRole(token: $token, request: $request) {
      token
    }
  }
`);

/**
 * A partial update: a field left out is kept, null clears it. The role form states its
 * full end state, so it sends all three.
 */
export async function updateTenantRole(
  token: string,
  request: { name?: string | null; description?: string | null; authorities?: string[] | null },
): Promise<void> {
  await gql('user-management', UPDATE_TENANT_ROLE, { token, request });
}

const DELETE_TENANT_ROLE = graphql(`
  mutation DeleteTenantRole($token: String!) {
    deleteTenantRole(token: $token)
  }
`);

export async function deleteTenantRole(token: string): Promise<boolean> {
  const data = await gql('user-management', DELETE_TENANT_ROLE, { token });
  return data.deleteTenantRole;
}

// ── Accepting an invitation (no session) ─────────────────────────────────

const ACCEPT_INVITATION = graphql(`
  mutation AcceptInvitation($code: String!, $password: String, $identityToken: String) {
    acceptInvitation(code: $code, password: $password, identityToken: $identityToken) {
      identityToken
      expiresAt
      superuser
      memberships {
        tenant
        roles
        ssoRequired
      }
    }
  }
`);

/**
 * Accept an invitation, giving exactly one of a password (to create a new account's
 * password) or the identity token of the existing account it was issued to. Answers what
 * login answers.
 */
export async function acceptInvitation(
  code: string,
  proof: { password: string } | { identityToken: string },
) {
  const variables: { code: string; password?: string; identityToken?: string } =
    'password' in proof ? { code, password: proof.password } : { code, identityToken: proof.identityToken };
  const data = await gql('user-management', ACCEPT_INVITATION, variables, { anonymous: true });
  return data.acceptInvitation;
}
