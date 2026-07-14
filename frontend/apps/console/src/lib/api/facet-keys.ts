// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management facet-key registry
// (ADR-061 G2). A facet key declares that an EntityAttribute key, for a member
// family, is a classification facet the console can offer as a browse/filter axis.
// It stores no values — those stay as EntityAttribute rows on the member entities.
// Addressed by its natural key (memberType, key); setFacetKey upserts on it.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { FacetKeysQuery } from '@/gql/device-management/graphql';

export type FacetKey = FacetKeysQuery['facetKeys']['results'][number];

// The member families a facet can classify (mirrors the backend GroupMemberTypes /
// entity.Type non-group set). The backend still validates.
export const FACET_MEMBER_TYPES = ['device', 'asset', 'area', 'customer'] as const;
export type FacetMemberType = (typeof FACET_MEMBER_TYPES)[number];

// The value types a facet's attribute values may carry (mirrors the backend
// AttributeValueType vocabulary).
export const FACET_VALUE_TYPES = ['STRING', 'LONG', 'DOUBLE', 'BOOLEAN', 'JSON'] as const;
export type FacetValueType = (typeof FACET_VALUE_TYPES)[number];

const FACET_KEYS = graphql(`
  query FacetKeys($criteria: FacetKeySearchCriteria!) {
    facetKeys(criteria: $criteria) {
      results {
        id
        memberType
        key
        valueType
        source
        values
        label
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// Facets per tenant are few; a single generous page avoids paging UI for v1.
export async function listFacetKeys(memberType?: string): Promise<FacetKey[]> {
  const data = await gql('device-management', FACET_KEYS, {
    criteria: { pageNumber: 1, pageSize: 500, memberType: memberType ?? null },
  });
  return data.facetKeys.results;
}

const SET_FACET_KEY = graphql(`
  mutation SetFacetKey($request: FacetKeySetRequest!) {
    setFacetKey(request: $request) {
      memberType
      key
    }
  }
`);

export async function setFacetKey(input: {
  memberType: string;
  key: string;
  valueType: string;
  values?: string[];
  label?: string;
}): Promise<{ memberType: string; key: string }> {
  const data = await gql('device-management', SET_FACET_KEY, {
    request: {
      memberType: input.memberType,
      key: input.key,
      valueType: input.valueType,
      // An empty vocabulary is a free-form facet: send null, not [].
      values: input.values && input.values.length > 0 ? input.values : null,
      label: input.label?.trim() ? input.label.trim() : null,
    },
  });
  return data.setFacetKey;
}

const DELETE_FACET_KEY = graphql(`
  mutation DeleteFacetKey($memberType: String!, $key: String!) {
    deleteFacetKey(memberType: $memberType, key: $key)
  }
`);

export async function deleteFacetKey(memberType: string, key: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_FACET_KEY, { memberType, key });
  return data.deleteFacetKey;
}
