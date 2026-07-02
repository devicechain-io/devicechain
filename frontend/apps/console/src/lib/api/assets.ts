// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  AssetsQuery,
  AssetTypesQuery,
  AssetGroupsQuery,
  AssetTypeCreateRequest,
  AssetGroupCreateRequest,
  AssetCreateRequest,
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Asset = AssetsQuery['assets']['results'][number];
export type AssetType = AssetTypesQuery['assetTypes']['results'][number];
export type Pagination = AssetsQuery['assets']['pagination'];
export type AssetSearchResults = AssetsQuery['assets'];
export type AssetTypeSearchResults = AssetTypesQuery['assetTypes'];
export type AssetGroup = AssetGroupsQuery['assetGroups']['results'][number];
export type AssetGroupSearchResults = AssetGroupsQuery['assetGroups'];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { AssetTypeCreateRequest, AssetGroupCreateRequest, AssetCreateRequest };

// ── Assets ──────────────────────────────────────────────────────────────

const ASSETS = graphql(`
  query Assets($criteria: AssetSearchCriteria!) {
    assets(criteria: $criteria) {
      results {
        id
        token
        name
        description
        createdAt
        assetType {
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

export async function listAssets(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<AssetSearchResults> {
  const data = await gql('device-management', ASSETS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.assets;
}

const ASSET_BY_TOKEN = graphql(`
  query AssetByToken($tokens: [String!]!) {
    assetsByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
      assetType {
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

export async function getAsset(token: string): Promise<Asset | null> {
  const data = await gql('device-management', ASSET_BY_TOKEN, { tokens: [token] });
  return data.assetsByToken[0] ?? null;
}

const CREATE_ASSET = graphql(`
  mutation CreateAsset($request: AssetCreateRequest) {
    createAsset(request: $request) {
      id
      token
      name
      description
      createdAt
      assetType {
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

export async function createAsset(request: AssetCreateRequest): Promise<Asset> {
  const data = await gql('device-management', CREATE_ASSET, { request });
  return data.createAsset;
}

const UPDATE_ASSET = graphql(`
  mutation UpdateAsset($token: String!, $request: AssetCreateRequest) {
    updateAsset(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
      assetType {
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

export async function updateAsset(token: string, request: AssetCreateRequest): Promise<Asset> {
  const data = await gql('device-management', UPDATE_ASSET, { token, request });
  return data.updateAsset;
}

const DELETE_ASSET = graphql(`
  mutation DeleteAsset($token: String!) {
    deleteAsset(token: $token)
  }
`);

export async function deleteAsset(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_ASSET, { token });
  return data.deleteAsset;
}

// ── Asset types ───────────────────────────────────────────────────────────

const ASSET_TYPES = graphql(`
  query AssetTypes($criteria: AssetTypeSearchCriteria!) {
    assetTypes(criteria: $criteria) {
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

export async function listAssetTypes(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<AssetTypeSearchResults> {
  const data = await gql('device-management', ASSET_TYPES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.assetTypes;
}

// The asset-type getter and mutations select the same shape as the AssetTypes
// query so their results stay assignable to the shared AssetType type.
const ASSET_TYPE_BY_TOKEN = graphql(`
  query AssetTypeByToken($tokens: [String!]!) {
    assetTypesByToken(tokens: $tokens) {
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

export async function getAssetType(token: string): Promise<AssetType | null> {
  const data = await gql('device-management', ASSET_TYPE_BY_TOKEN, { tokens: [token] });
  return data.assetTypesByToken[0] ?? null;
}

const CREATE_ASSET_TYPE = graphql(`
  mutation CreateAssetType($request: AssetTypeCreateRequest) {
    createAssetType(request: $request) {
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

export async function createAssetType(request: AssetTypeCreateRequest): Promise<AssetType> {
  const data = await gql('device-management', CREATE_ASSET_TYPE, { request });
  return data.createAssetType;
}

const UPDATE_ASSET_TYPE = graphql(`
  mutation UpdateAssetType($token: String!, $request: AssetTypeCreateRequest) {
    updateAssetType(token: $token, request: $request) {
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

export async function updateAssetType(
  token: string,
  request: AssetTypeCreateRequest,
): Promise<AssetType> {
  const data = await gql('device-management', UPDATE_ASSET_TYPE, { token, request });
  return data.updateAssetType;
}

const DELETE_ASSET_TYPE = graphql(`
  mutation DeleteAssetType($token: String!) {
    deleteAssetType(token: $token)
  }
`);

export async function deleteAssetType(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_ASSET_TYPE, { token });
  return data.deleteAssetType;
}

// ── Asset groups ──────────────────────────────────────────────────────────

const ASSET_GROUPS = graphql(`
  query AssetGroups($criteria: AssetGroupSearchCriteria!) {
    assetGroups(criteria: $criteria) {
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

export async function listAssetGroups(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<AssetGroupSearchResults> {
  const data = await gql('device-management', ASSET_GROUPS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.assetGroups;
}

// The asset-group getter and mutations select the same shape as the AssetGroups
// query so their results stay assignable to the shared AssetGroup type.
const ASSET_GROUP_BY_TOKEN = graphql(`
  query AssetGroupByToken($tokens: [String!]!) {
    assetGroupsByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function getAssetGroup(token: string): Promise<AssetGroup | null> {
  const data = await gql('device-management', ASSET_GROUP_BY_TOKEN, { tokens: [token] });
  return data.assetGroupsByToken[0] ?? null;
}

const CREATE_ASSET_GROUP = graphql(`
  mutation CreateAssetGroup($request: AssetGroupCreateRequest) {
    createAssetGroup(request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function createAssetGroup(request: AssetGroupCreateRequest): Promise<AssetGroup> {
  const data = await gql('device-management', CREATE_ASSET_GROUP, { request });
  return data.createAssetGroup;
}

const UPDATE_ASSET_GROUP = graphql(`
  mutation UpdateAssetGroup($token: String!, $request: AssetGroupCreateRequest) {
    updateAssetGroup(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function updateAssetGroup(
  token: string,
  request: AssetGroupCreateRequest,
): Promise<AssetGroup> {
  const data = await gql('device-management', UPDATE_ASSET_GROUP, { token, request });
  return data.updateAssetGroup;
}

const DELETE_ASSET_GROUP = graphql(`
  mutation DeleteAssetGroup($token: String!) {
    deleteAssetGroup(token: $token)
  }
`);

export async function deleteAssetGroup(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_ASSET_GROUP, { token });
  return data.deleteAssetGroup;
}
