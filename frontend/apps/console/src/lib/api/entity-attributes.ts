// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations over an entity's current-state attributes (ADR-012) —
// the rows that ARE a facet's values (ADR-061 SD-1). The facet-key registry
// (lib/api/facet-keys.ts) declares which attribute keys are classification axes; it
// stores no values. This module is the other half: reading and writing the values
// themselves, which until now nothing we ship could do from inside the product.
//
// 🔴 FACET_VALUE_SCOPE IS NOT A DEFAULT, IT IS THE ONLY SCOPE BROWSE CAN SEE.
// The selector lowering pins facets to ONE EntityAttribute scope, and that scope is
// SHARED (backend/services/device-management/internal/selector/lower.go — "FacetScope
// pins which EntityAttribute scope defines facets. v1 = SHARED"; api_group_members.go
// passes `string(AttributeScopeShared)`). Writing the right key at CLIENT or SERVER
// scope is perfectly VALID, raises no error anywhere, and matches NOTHING — Browse
// says "matches 0" behind a form that looked like it saved. That failure is the
// reason this module exists, so the scope is a named constant used by every write
// rather than a literal repeated at three call sites.
//
// Unlike every neighbouring registry update in this codebase, setEntityAttribute is
// an UPSERT ON THE NATURAL KEY (entityType, entity, scope, attrKey) — not a full
// replace of the entity's attribute set. Writing one attribute cannot drop another,
// so this carries none of the field-loss hazard the registry forms have to guard
// against by re-sending everything they are not editing.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { EntityAttributesQuery } from '@/gql/device-management/graphql';

export type EntityAttribute = EntityAttributesQuery['entityAttributes']['results'][number];

// The one scope a facet selector reads. See the block comment above before changing it.
export const FACET_VALUE_SCOPE = 'SHARED';

const ENTITY_ATTRIBUTES = graphql(`
  query EntityAttributes($criteria: EntityAttributeSearchCriteria!) {
    entityAttributes(criteria: $criteria) {
      results {
        id
        entityType
        scope
        attrKey
        valueType
        value
        lastUpdated
      }
      pagination {
        totalRecords
      }
    }
  }
`);

// listEntityAttributes returns every attribute an entity carries, in every scope —
// the panel shows the non-SHARED ones read-only precisely so "I set climate and Browse
// still says 0" has a visible explanation when the device reported its own `climate`.
// An entity's attributes are few; one generous page avoids paging UI.
export async function listEntityAttributes(
  entityType: string,
  entity: string,
): Promise<EntityAttribute[]> {
  const data = await gql('device-management', ENTITY_ATTRIBUTES, {
    criteria: { pageNumber: 1, pageSize: 500, entityType, entity },
  });
  return data.entityAttributes.results;
}

const SET_ENTITY_ATTRIBUTE = graphql(`
  mutation SetEntityAttribute($request: EntityAttributeSetRequest!) {
    setEntityAttribute(request: $request) {
      id
      scope
      attrKey
      valueType
      value
    }
  }
`);

// setFacetValue writes one attribute at the facet scope. `valueType` is the value type
// the facet key DECLARES, not a guess from the text: the lowering pins a scalar leaf to
// a value_type (`ea.value_type = 'STRING'`, or `IN ('LONG','DOUBLE')` for a numeric
// comparison), so a value stored under the wrong type is a row that exists, reads back
// fine, and is invisible to the very axis it was authored for.
export async function setFacetValue(input: {
  entityType: string;
  entity: string;
  attrKey: string;
  valueType: string;
  value: string;
}): Promise<void> {
  await gql('device-management', SET_ENTITY_ATTRIBUTE, {
    request: {
      entityType: input.entityType,
      entity: input.entity,
      scope: FACET_VALUE_SCOPE,
      attrKey: input.attrKey,
      valueType: input.valueType,
      value: input.value,
    },
  });
}

const DELETE_ENTITY_ATTRIBUTE = graphql(`
  mutation DeleteEntityAttribute(
    $entityType: String!
    $entity: String!
    $scope: String!
    $attrKey: String!
  ) {
    deleteEntityAttribute(
      entityType: $entityType
      entity: $entity
      scope: $scope
      attrKey: $attrKey
    )
  }
`);

// clearFacetValue removes the attribute row. Clearing a facet is a DELETE, never a
// write of "": an empty-string attribute is a row that still exists, still satisfies
// `"climate" in attr`, and would match `attr["climate"] == ""` — none of which is what
// "this entity has no climate" means.
export async function clearFacetValue(input: {
  entityType: string;
  entity: string;
  attrKey: string;
  scope?: string;
}): Promise<boolean> {
  const data = await gql('device-management', DELETE_ENTITY_ATTRIBUTE, {
    entityType: input.entityType,
    entity: input.entity,
    scope: input.scope ?? FACET_VALUE_SCOPE,
    attrKey: input.attrKey,
  });
  return data.deleteEntityAttribute;
}
