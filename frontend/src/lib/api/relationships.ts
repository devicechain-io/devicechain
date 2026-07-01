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

// The reserved relationship-type token for device assignment — tracked, so a
// device's primary assignment is denormalized onto its events as their anchor
// (ADR-013 addendum). The backend auto-provisions it per tenant on first use.
const ASSIGNED = 'assigned';

// The entity types a device can be assigned to (uniform entity references, ADR-013).
export const ASSIGNMENT_TARGET_TYPES = ['customer', 'area', 'asset'] as const;
export type AssignmentTargetType = (typeof ASSIGNMENT_TARGET_TYPES)[number];

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

// List a device's assignments: tracked edges from the device (source) of the
// reserved "assigned" type, each targeting a customer/area/asset. The lowest-id
// edge is the primary anchor denormalized onto the device's events. Requires
// device:read.
export async function listDeviceAssignments(deviceToken: string): Promise<EntityRelationship[]> {
  const data = await gql('device-management', ENTITY_RELATIONSHIPS, {
    criteria: {
      sourceType: 'device',
      source: deviceToken,
      relationshipType: ASSIGNED,
      pageNumber: 1,
      pageSize: 100,
    },
  });
  return data.entityRelationships.results;
}

// Assign a device to a target entity (customer/area/asset). Reuses the bulk create
// so the reserved "assigned" type is auto-provisioned per tenant on first use.
// Requires device:write.
export async function assignDevice(
  deviceToken: string,
  targetType: AssignmentTargetType,
  targetToken: string,
): Promise<number> {
  const data = await gql('device-management', CREATE_ENTITY_RELATIONSHIPS, {
    requests: [
      {
        token: crypto.randomUUID(),
        sourceType: 'device',
        source: deviceToken,
        targetType,
        target: targetToken,
        relationshipType: ASSIGNED,
      },
    ],
  });
  return data.createEntityRelationships.length;
}

// Remove a device assignment by its edge token. Requires device:write.
export async function unassignDevice(edgeToken: string): Promise<boolean> {
  const data = await gql('device-management', REMOVE_ENTITY_RELATIONSHIPS, { tokens: [edgeToken] });
  return data.removeEntityRelationships;
}
