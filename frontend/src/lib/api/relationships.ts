// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed operations over the uniform entity-relationship graph (ADR-013), plus
// group-membership helpers built on top: membership is just a group -> member
// edge of the reserved "member" relationship type, which the device-management
// backend auto-provisions per tenant on first use.
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/device-management';
import type { EntityRelationshipsQuery } from '@/gql/device-management/graphql';

export type EntityRelationship = EntityRelationshipsQuery['entityRelationships']['results'][number];
export type EntityRelationshipSearchResults = EntityRelationshipsQuery['entityRelationships'];

// The reserved relationship-type token for group membership (see the backend's
// MembershipRelationshipType).
const MEMBER = 'member';

const ENTITY_RELATIONSHIPS = graphql(`
  query EntityRelationships($criteria: EntityRelationshipSearchCriteria!) {
    entityRelationships(criteria: $criteria) {
      results {
        id
        token
        targetType
        target {
          id
          token
        }
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// List a group's members: edges from the group (source) of the member type. The
// edge's `token` identifies it for removal; `target.token` is the member entity.
export async function listGroupMembers(
  groupType: string,
  groupToken: string,
  opts: { pageNumber: number; pageSize: number },
): Promise<EntityRelationshipSearchResults> {
  const data = await gql('device-management', ENTITY_RELATIONSHIPS, {
    criteria: {
      sourceType: groupType,
      source: groupToken,
      relationshipType: MEMBER,
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.entityRelationships;
}

const CREATE_ENTITY_RELATIONSHIPS = graphql(`
  mutation CreateEntityRelationships($requests: [EntityRelationshipCreateRequest!]!) {
    createEntityRelationships(requests: $requests) {
      id
      token
    }
  }
`);

// Add members to a group in one transaction. Each edge needs a unique token; we
// mint a client-side UUID per edge.
export async function addGroupMembers(
  groupType: string,
  groupToken: string,
  memberType: string,
  memberTokens: string[],
): Promise<number> {
  if (memberTokens.length === 0) return 0;
  const requests = memberTokens.map((target) => ({
    token: crypto.randomUUID(),
    sourceType: groupType,
    source: groupToken,
    targetType: memberType,
    target,
    relationshipType: MEMBER,
  }));
  const data = await gql('device-management', CREATE_ENTITY_RELATIONSHIPS, { requests });
  return data.createEntityRelationships.length;
}

const REMOVE_ENTITY_RELATIONSHIPS = graphql(`
  mutation RemoveEntityRelationships($tokens: [String!]!) {
    removeEntityRelationships(tokens: $tokens)
  }
`);

// Remove members by their edge tokens.
export async function removeGroupMembers(edgeTokens: string[]): Promise<boolean> {
  if (edgeTokens.length === 0) return false;
  const data = await gql('device-management', REMOVE_ENTITY_RELATIONSHIPS, { tokens: edgeTokens });
  return data.removeEntityRelationships;
}
