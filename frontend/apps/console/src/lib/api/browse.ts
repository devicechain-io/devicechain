// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for the faceted browse + dynamic-group consumer (ADR-061
// G4). These sit over the G3 selector engine: previewSelector evaluates a CANDIDATE
// selector without saving it (the live "matches N" preview), a dynamic EntityGroup is
// created with membershipMode="dynamic" + the composed selector, and a group's members
// resolve transparently over either mode (static edges | dynamic eval-on-read).
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  PreviewSelectorQuery,
  GroupMembersQuery,
  DynamicGroupsQuery,
} from '@/gql/device-management/graphql';
import { createEntityGroup, type EntityGroup } from './device-management';

// A resolved group member — the lightweight identity of a family entity.
export type GroupMember = GroupMembersQuery['entityGroupsByToken'][number]['members']['results'][number];

// A saved dynamic group as listed on the browse screen (carries its selector).
export type DynamicGroup = DynamicGroupsQuery['entityGroups']['results'][number];

// The outcome of previewing a candidate selector: valid + matches, or an inline error.
export type SelectorPreview = PreviewSelectorQuery['previewSelector'];

const PREVIEW_SELECTOR = graphql(`
  query PreviewSelector($memberType: String!, $selector: String!, $pagination: PaginationInput!) {
    previewSelector(memberType: $memberType, selector: $selector, pagination: $pagination) {
      valid
      error
      members {
        results {
          id
          token
        }
        pagination {
          pageStart
          pageEnd
          totalRecords
        }
      }
    }
  }
`);

// previewSelector compiles + cost-gates + lowers a candidate selector WITHOUT saving a
// group and returns its matches. A non-lowerable / over-budget / malformed selector
// comes back as { valid: false, error } — surfaced inline, not thrown — so the caller
// shows it as the user edits. A genuine fault (auth, network) still rejects.
export async function previewSelector(
  memberType: string,
  selector: string,
  pageSize = 25,
): Promise<SelectorPreview> {
  const data = await gql('device-management', PREVIEW_SELECTOR, {
    memberType,
    selector,
    pagination: { pageNumber: 1, pageSize },
  });
  return data.previewSelector;
}

const DYNAMIC_GROUPS = graphql(`
  query DynamicGroups($memberType: String!) {
    entityGroups(criteria: { pageNumber: 1, pageSize: 200, memberType: $memberType, membershipMode: "dynamic" }) {
      results {
        id
        token
        name
        memberType
        membershipMode
        selector
      }
      pagination {
        totalRecords
      }
    }
  }
`);

// listDynamicGroups returns a family's saved dynamic groups (with their selectors) for
// the browse screen's saved-group list. Groups per family are few; one page suffices.
export async function listDynamicGroups(memberType: string): Promise<DynamicGroup[]> {
  const data = await gql('device-management', DYNAMIC_GROUPS, { memberType });
  return data.entityGroups.results;
}

const GROUP_MEMBERS = graphql(`
  query GroupMembers($tokens: [String!]!, $pagination: PaginationInput!) {
    entityGroupsByToken(tokens: $tokens) {
      token
      members(pagination: $pagination) {
        results {
          id
          token
        }
        pagination {
          pageStart
          pageEnd
          totalRecords
        }
      }
    }
  }
`);

export interface GroupMembersPage {
  results: GroupMember[];
  totalRecords: number;
}

// resolveGroupMembers pages a saved group's members (static edges or dynamic matches;
// the caller does not branch on mode). Returns an empty page if the token is unknown.
export async function resolveGroupMembers(
  token: string,
  opts: { pageNumber: number; pageSize: number },
): Promise<GroupMembersPage> {
  const data = await gql('device-management', GROUP_MEMBERS, {
    tokens: [token],
    pagination: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  const group = data.entityGroupsByToken[0];
  if (!group) return { results: [], totalRecords: 0 };
  return {
    results: group.members.results,
    totalRecords: group.members.pagination.totalRecords ?? 0,
  };
}

// createDynamicGroup saves a composed selector as a dynamic EntityGroup. The backend
// re-compiles + cost-gates the selector at create, so an invalid selector rejects here
// exactly as it would have in the preview. Presentation fields are optional.
export async function createDynamicGroup(input: {
  memberType: string;
  selector: string;
  token: string;
  name?: string;
  description?: string;
}): Promise<EntityGroup> {
  return createEntityGroup({
    memberType: input.memberType,
    membershipMode: 'dynamic',
    selector: input.selector,
    token: input.token,
    name: input.name?.trim() || null,
    description: input.description?.trim() || null,
  });
}
