// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  AreasQuery,
  AreaTypesQuery,
  AreaTypeCreateRequest,
  AreaCreateRequest,
} from '@/gql/device-management/graphql';
import {
  listEntityGroups,
  getEntityGroupOfType,
  createEntityGroup,
  updateEntityGroup,
  deleteEntityGroup,
  type EntityGroup,
  type GroupFormRequest,
} from './device-management';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Area = AreasQuery['areas']['results'][number];
export type AreaType = AreaTypesQuery['areaTypes']['results'][number];
// Area groups are the uniform EntityGroup filtered to memberType='area' (ADR-061).
export type AreaGroup = EntityGroup;
export type Pagination = AreasQuery['areas']['pagination'];
export type AreaSearchResults = AreasQuery['areas'];
export type AreaTypeSearchResults = AreaTypesQuery['areaTypes'];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { AreaTypeCreateRequest, AreaCreateRequest };

// ── Areas ───────────────────────────────────────────────────────────────

const AREAS = graphql(`
  query Areas($criteria: AreaSearchCriteria!) {
    areas(criteria: $criteria) {
      results {
        id
        token
        name
        description
        metadata
        createdAt
        areaType {
          id
          token
          name
          icon
          backgroundColor
          foregroundColor
          borderColor
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

export async function listAreas(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<AreaSearchResults> {
  const data = await gql('device-management', AREAS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.areas;
}

const AREA_BY_TOKEN = graphql(`
  query AreaByToken($tokens: [String!]!) {
    areasByToken(tokens: $tokens) {
      id
      token
      name
      description
      metadata
      createdAt
      areaType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function getArea(token: string): Promise<Area | null> {
  const data = await gql('device-management', AREA_BY_TOKEN, { tokens: [token] });
  return data.areasByToken[0] ?? null;
}

const CREATE_AREA = graphql(`
  mutation CreateArea($request: AreaCreateRequest) {
    createArea(request: $request) {
      id
      token
      name
      description
      metadata
      createdAt
      areaType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function createArea(request: AreaCreateRequest): Promise<Area> {
  const data = await gql('device-management', CREATE_AREA, { request });
  return data.createArea;
}

const UPDATE_AREA = graphql(`
  mutation UpdateArea($token: String!, $request: AreaCreateRequest) {
    updateArea(token: $token, request: $request) {
      id
      token
      name
      description
      metadata
      createdAt
      areaType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

// 🔴 `Required<…>` is not decoration: it makes the OMISSION a compile error.
// The update is a full replace, so the defect this whole file guards against is a
// caller that simply does not mention a field. Typing the parameter as the plain
// request — where every field is optional — is what let that compile. Requiring
// every key forces each caller to say what it wants done with each one, and
// `…Preserved(entity)` is how it says "leave this as it was".
export async function updateArea(token: string, request: Required<AreaCreateRequest>): Promise<Area> {
  const data = await gql('device-management', UPDATE_AREA, { token, request });
  return data.updateArea;
}

// 🔴 An area update is a FULL REPLACE: the stored record is rebuilt from the
// request, so a field the request omits is DELETED. Every edit therefore starts
// from this projection of what the entity already is, and overrides only what the
// form edits. The `Required<…>` RETURN TYPE is the gate that keeps it honest — add a
// field to the schema and codegen widens the request type, which breaks this
// function until someone decides what an edit should do with it. Without that,
// the new field would simply start being erased, silently and successfully.
//
// areaTypeToken is deliberately NOT preserved here: it is a required field of the
// form, so every caller supplies it and the request type refuses to compile
// without it. Defaulting it would only be a way to send a wrong one.
export function areaPreserved(a: Area): Required<Omit<AreaCreateRequest, 'areaTypeToken'>> {
  // `?? null` rather than `?? undefined`: for a full replace an explicit null and
  // an omitted field both land as a nil *string server-side, and null is the one
  // that says so out loud.
  return {
    token: a.token,
    name: a.name ?? null,
    description: a.description ?? null,
    metadata: a.metadata ?? null,
  };
}

const DELETE_AREA = graphql(`
  mutation DeleteArea($token: String!) {
    deleteArea(token: $token)
  }
`);

export async function deleteArea(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_AREA, { token });
  return data.deleteArea;
}

// ── Area types ──────────────────────────────────────────────────────────

const AREA_TYPES = graphql(`
  query AreaTypes($criteria: AreaTypeSearchCriteria!) {
    areaTypes(criteria: $criteria) {
      results {
        id
        token
        name
        description
        icon
        backgroundColor
        foregroundColor
        borderColor
        imageUrl
        metadata
        createdAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listAreaTypes(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<AreaTypeSearchResults> {
  const data = await gql('device-management', AREA_TYPES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.areaTypes;
}

// The area-type getter and mutations select the same shape as the AreaTypes
// query so their results stay assignable to the shared AreaType type.
const AREA_TYPE_BY_TOKEN = graphql(`
  query AreaTypeByToken($tokens: [String!]!) {
    areaTypesByToken(tokens: $tokens) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function getAreaType(token: string): Promise<AreaType | null> {
  const data = await gql('device-management', AREA_TYPE_BY_TOKEN, { tokens: [token] });
  return data.areaTypesByToken[0] ?? null;
}

const CREATE_AREA_TYPE = graphql(`
  mutation CreateAreaType($request: AreaTypeCreateRequest) {
    createAreaType(request: $request) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function createAreaType(request: AreaTypeCreateRequest): Promise<AreaType> {
  const data = await gql('device-management', CREATE_AREA_TYPE, { request });
  return data.createAreaType;
}

const UPDATE_AREA_TYPE = graphql(`
  mutation UpdateAreaType($token: String!, $request: AreaTypeCreateRequest) {
    updateAreaType(token: $token, request: $request) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function updateAreaType(
  token: string,
  request: Required<AreaTypeCreateRequest>,
): Promise<AreaType> {
  const data = await gql('device-management', UPDATE_AREA_TYPE, { token, request });
  return data.updateAreaType;
}

// The area-type counterpart of areaPreserved: an area-type update is a full
// replace too, and this form edits only name + description while the Appearance
// tab edits only icon + colors. Each starts from here so the other's fields — and
// the imageUrl and metadata no console form edits at all — survive the save.
export function areaTypePreserved(at: AreaType): Required<AreaTypeCreateRequest> {
  return {
    token: at.token,
    name: at.name ?? null,
    description: at.description ?? null,
    icon: at.icon ?? null,
    backgroundColor: at.backgroundColor ?? null,
    foregroundColor: at.foregroundColor ?? null,
    borderColor: at.borderColor ?? null,
    imageUrl: at.imageUrl ?? null,
    metadata: at.metadata ?? null,
  };
}

const DELETE_AREA_TYPE = graphql(`
  mutation DeleteAreaType($token: String!) {
    deleteAreaType(token: $token)
  }
`);

export async function deleteAreaType(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_AREA_TYPE, { token });
  return data.deleteAreaType;
}

// ── Area groups (memberType = 'area') ───────────────────────────────────────
// Thin wrappers over the uniform EntityGroup operations (ADR-061), baking in the
// area member family. See device-management.ts for the canonical group ops.

export const listAreaGroups = (opts: { pageNumber: number; pageSize: number }) =>
  listEntityGroups({ ...opts, memberType: 'area' });
export const getAreaGroup = (token: string) => getEntityGroupOfType(token, 'area');
export const createAreaGroup = (request: GroupFormRequest) =>
  createEntityGroup({ ...request, memberType: 'area' });
export const updateAreaGroup = (token: string, request: Required<GroupFormRequest>) =>
  updateEntityGroup(token, { ...request, memberType: 'area' });
export const deleteAreaGroup = deleteEntityGroup;
