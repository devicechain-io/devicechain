// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  AreasQuery,
  AreaTypesQuery,
  AreaGroupsQuery,
  AreaTypeCreateRequest,
  AreaGroupCreateRequest,
  AreaCreateRequest,
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Area = AreasQuery['areas']['results'][number];
export type AreaType = AreaTypesQuery['areaTypes']['results'][number];
export type AreaGroup = AreaGroupsQuery['areaGroups']['results'][number];
export type Pagination = AreasQuery['areas']['pagination'];
export type AreaSearchResults = AreasQuery['areas'];
export type AreaTypeSearchResults = AreaTypesQuery['areaTypes'];
export type AreaGroupSearchResults = AreaGroupsQuery['areaGroups'];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { AreaTypeCreateRequest, AreaGroupCreateRequest, AreaCreateRequest };

// ── Areas ───────────────────────────────────────────────────────────────

const AREAS = graphql(`
  query Areas($criteria: AreaSearchCriteria!) {
    areas(criteria: $criteria) {
      results {
        id
        token
        name
        description
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

export async function updateArea(token: string, request: AreaCreateRequest): Promise<Area> {
  const data = await gql('device-management', UPDATE_AREA, { token, request });
  return data.updateArea;
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
      createdAt
    }
  }
`);

export async function updateAreaType(
  token: string,
  request: AreaTypeCreateRequest,
): Promise<AreaType> {
  const data = await gql('device-management', UPDATE_AREA_TYPE, { token, request });
  return data.updateAreaType;
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

// ── Area groups ─────────────────────────────────────────────────────────

const AREA_GROUPS = graphql(`
  query AreaGroups($criteria: AreaGroupSearchCriteria!) {
    areaGroups(criteria: $criteria) {
      results {
        id
        token
        name
        description
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

export async function listAreaGroups(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<AreaGroupSearchResults> {
  const data = await gql('device-management', AREA_GROUPS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.areaGroups;
}

// The area-group getter and mutations select the same shape as the AreaGroups
// query so their results stay assignable to the shared AreaGroup type.
const AREA_GROUP_BY_TOKEN = graphql(`
  query AreaGroupByToken($tokens: [String!]!) {
    areaGroupsByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function getAreaGroup(token: string): Promise<AreaGroup | null> {
  const data = await gql('device-management', AREA_GROUP_BY_TOKEN, { tokens: [token] });
  return data.areaGroupsByToken[0] ?? null;
}

const CREATE_AREA_GROUP = graphql(`
  mutation CreateAreaGroup($request: AreaGroupCreateRequest) {
    createAreaGroup(request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function createAreaGroup(request: AreaGroupCreateRequest): Promise<AreaGroup> {
  const data = await gql('device-management', CREATE_AREA_GROUP, { request });
  return data.createAreaGroup;
}

const UPDATE_AREA_GROUP = graphql(`
  mutation UpdateAreaGroup($token: String!, $request: AreaGroupCreateRequest) {
    updateAreaGroup(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function updateAreaGroup(
  token: string,
  request: AreaGroupCreateRequest,
): Promise<AreaGroup> {
  const data = await gql('device-management', UPDATE_AREA_GROUP, { token, request });
  return data.updateAreaGroup;
}

const DELETE_AREA_GROUP = graphql(`
  mutation DeleteAreaGroup($token: String!) {
    deleteAreaGroup(token: $token)
  }
`);

export async function deleteAreaGroup(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_AREA_GROUP, { token });
  return data.deleteAreaGroup;
}
