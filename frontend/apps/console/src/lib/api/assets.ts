// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  AssetsQuery,
  AssetTypesQuery,
  AssetTypeCreateRequest,
  AssetCreateRequest,
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
export type Asset = AssetsQuery['assets']['results'][number];
export type AssetType = AssetTypesQuery['assetTypes']['results'][number];
export type Pagination = AssetsQuery['assets']['pagination'];
export type AssetSearchResults = AssetsQuery['assets'];
export type AssetTypeSearchResults = AssetTypesQuery['assetTypes'];
// Asset groups are the uniform EntityGroup filtered to memberType='asset' (ADR-061).
export type AssetGroup = EntityGroup;

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { AssetTypeCreateRequest, AssetCreateRequest };

// ── Assets ──────────────────────────────────────────────────────────────

const ASSETS = graphql(`
  query Assets($criteria: AssetSearchCriteria!) {
    assets(criteria: $criteria) {
      results {
        id
        token
        name
        description
        metadata
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
      metadata
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
      metadata
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
      metadata
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

// 🔴 `Required<…>` is not decoration: it makes the OMISSION a compile error.
// The update is a full replace, so the defect this whole file guards against is a
// caller that simply does not mention a field. Typing the parameter as the plain
// request — where every field is optional — is what let that compile. Requiring
// every key forces each caller to say what it wants done with each one, and
// `…Preserved(entity)` is how it says "leave this as it was".
export async function updateAsset(token: string, request: Required<AssetCreateRequest>): Promise<Asset> {
  const data = await gql('device-management', UPDATE_ASSET, { token, request });
  return data.updateAsset;
}

// 🔴 An asset update is a FULL REPLACE: the stored record is rebuilt from the
// request, so a field the request omits is DELETED. Every edit therefore starts
// from this projection of what the entity already is, and overrides only what the
// form edits. The `Required<…>` RETURN TYPE is the gate that keeps it honest — add a
// field to the schema and codegen widens the request type, which breaks this
// function until someone decides what an edit should do with it. Without that,
// the new field would simply start being erased, silently and successfully.
//
// assetTypeToken is deliberately NOT preserved here: it is a required field of the
// form, so every caller supplies it and the request type refuses to compile
// without it. Defaulting it would only be a way to send a wrong one.
export function assetPreserved(a: Asset): Required<Omit<AssetCreateRequest, 'assetTypeToken'>> {
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
      imageUrl
      metadata
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
      imageUrl
      metadata
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
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function updateAssetType(
  token: string,
  request: Required<AssetTypeCreateRequest>,
): Promise<AssetType> {
  const data = await gql('device-management', UPDATE_ASSET_TYPE, { token, request });
  return data.updateAssetType;
}

// The asset-type counterpart of assetPreserved: an asset-type update is a full
// replace too, and this form edits only name + description while the Appearance
// tab edits only icon + colors. Each starts from here so the other's fields — and
// the imageUrl and metadata no console form edits at all — survive the save.
export function assetTypePreserved(at: AssetType): Required<AssetTypeCreateRequest> {
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

const DELETE_ASSET_TYPE = graphql(`
  mutation DeleteAssetType($token: String!) {
    deleteAssetType(token: $token)
  }
`);

export async function deleteAssetType(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_ASSET_TYPE, { token });
  return data.deleteAssetType;
}

// ── Asset groups (memberType = 'asset') ─────────────────────────────────────
// Thin wrappers over the uniform EntityGroup operations (ADR-061), baking in the
// asset member family. See device-management.ts for the canonical group ops.

export const listAssetGroups = (opts: { pageNumber: number; pageSize: number }) =>
  listEntityGroups({ ...opts, memberType: 'asset' });
export const getAssetGroup = (token: string) => getEntityGroupOfType(token, 'asset');
export const createAssetGroup = (request: GroupFormRequest) =>
  createEntityGroup({ ...request, memberType: 'asset' });
export const updateAssetGroup = (token: string, request: Required<GroupFormRequest>) =>
  updateEntityGroup(token, { ...request, memberType: 'asset' });
export const deleteAssetGroup = deleteEntityGroup;
