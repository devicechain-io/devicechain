// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  AssetsQuery,
  AssetTypesQuery,
  AssetTypeVersionsQuery,
  ActiveAssetTypeVersionQuery,
  AssetTypeCreateRequest,
  AssetCreateRequest,
  AssetTypeUpdateRequest,
  AssetUpdateRequest,
} from '@/gql/device-management/graphql';
import {
  listEntityGroups,
  getEntityGroupOfType,
  createEntityGroup,
  updateEntityGroup,
  deleteEntityGroup,
  type EntityGroup,
  type GroupFormRequest,
  type EntityGroupUpdateRequest,
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
export type {
  AssetTypeCreateRequest,
  AssetCreateRequest,
  AssetTypeUpdateRequest,
  AssetUpdateRequest,
};

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
        properties
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
      properties
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
      properties
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
  mutation UpdateAsset($token: String!, $request: AssetUpdateRequest!) {
    updateAsset(token: $token, request: $request) {
      id
      token
      name
      description
      metadata
      properties
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

// updateAsset is a PARTIAL update: pass only the fields being changed. An omitted
// field is left alone, an explicit null clears it.
//
// 🔴 Callers must NOT carry forward the fields they do not edit. The `assetPreserved`
// projection that used to sit here was the right fix for a full replace and is the
// WRONG one now: a form that re-sends fields it never showed is writing them back
// from a snapshot it read minutes ago, so two operators on two tabs each silently
// overwrite the other. Absence is the carry-forward.
export async function updateAsset(
  token: string,
  request: AssetUpdateRequest,
): Promise<Asset> {
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
        imageUrl
        metadata
        propertySchema
        activeVersion
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
      propertySchema
      activeVersion
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
      propertySchema
      activeVersion
      createdAt
    }
  }
`);

export async function createAssetType(request: AssetTypeCreateRequest): Promise<AssetType> {
  const data = await gql('device-management', CREATE_ASSET_TYPE, { request });
  return data.createAssetType;
}

const UPDATE_ASSET_TYPE = graphql(`
  mutation UpdateAssetType($token: String!, $request: AssetTypeUpdateRequest!) {
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
      propertySchema
      activeVersion
      createdAt
    }
  }
`);

export async function updateAssetType(
  token: string,
  request: AssetTypeUpdateRequest,
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

// ── Asset groups (memberType = 'asset') ─────────────────────────────────────
// Thin wrappers over the uniform EntityGroup operations (ADR-061), baking in the
// asset member family. See device-management.ts for the canonical group ops.

export const listAssetGroups = (opts: { pageNumber: number; pageSize: number }) =>
  listEntityGroups({ ...opts, memberType: 'asset' });
export const getAssetGroup = (token: string) => getEntityGroupOfType(token, 'asset');
export const createAssetGroup = (request: GroupFormRequest) =>
  createEntityGroup({ ...request, memberType: 'asset' });
export const updateAssetGroup = (token: string, request: EntityGroupUpdateRequest) =>
  updateEntityGroup(token, request);
export const deleteAssetGroup = deleteEntityGroup;

// ── Asset hierarchy (parent/child over the relationship graph) ──────────────
// The hierarchy is not a column on Asset: it is an edge of the reserved
// "contains" relationship type, which the backend auto-provisions per tenant.
// These operations are the whole of the console's dealings with it — nothing here
// hand-builds a containment edge, so the structural contract the server enforces
// (one parent, no cycle, both ends assets, bounded depth) is the only thing
// shaping the tree.

const ASSET_PARENT = graphql(`
  query AssetParent($token: String!) {
    assetParent(token: $token) {
      id
      token
      name
    }
  }
`);

// The asset directly above this one, or null when it is a root. A root is the
// normal state for most assets, so null here is an answer and not a failure.
export async function getAssetParent(token: string) {
  const data = await gql('device-management', ASSET_PARENT, { token });
  return data.assetParent ?? null;
}

const ASSET_ANCESTORS = graphql(`
  query AssetAncestors($token: String!) {
    assetAncestors(token: $token) {
      id
      token
      name
    }
  }
`);

// The path to the root, NEAREST ANCESTOR FIRST — the order a breadcrumb is built
// from. It is not reversed here, so the array matches what the server documents;
// the panel that renders root-first reverses a copy.
export async function listAssetAncestors(token: string) {
  const data = await gql('device-management', ASSET_ANCESTORS, { token });
  return data.assetAncestors;
}

const ASSET_CHILDREN = graphql(`
  query AssetChildren($parentToken: String, $pagination: PaginationInput!) {
    assetChildren(parentToken: $parentToken, pagination: $pagination) {
      results {
        id
        token
        name
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

// A page of the assets directly below a parent, or the tree's ROOTS when
// parentToken is null. One call for both, because a tree browser asks the same
// question at every level.
export async function listAssetChildren(
  parentToken: string | null,
  pagination: { pageNumber: number; pageSize: number },
) {
  const data = await gql('device-management', ASSET_CHILDREN, {
    parentToken: parentToken ?? undefined,
    pagination,
  });
  return data.assetChildren;
}

const SET_ASSET_PARENT = graphql(`
  mutation SetAssetParent($childToken: String!, $parentToken: String!) {
    setAssetParent(childToken: $childToken, parentToken: $parentToken) {
      id
      token
    }
  }
`);

// Place an asset under a parent, replacing whatever parent it had. The server
// refuses a self-parent, a cycle and an over-deep tree; the caller surfaces the
// message rather than pre-checking, so the console and the API can never disagree
// about what is legal.
export async function setAssetParent(childToken: string, parentToken: string) {
  const data = await gql('device-management', SET_ASSET_PARENT, { childToken, parentToken });
  return data.setAssetParent;
}

const CLEAR_ASSET_PARENT = graphql(`
  mutation ClearAssetParent($childToken: String!) {
    clearAssetParent(childToken: $childToken)
  }
`);

// Detach an asset from its parent, making it a root. Its own children travel with
// it — this moves one edge, not a subtree.
export async function clearAssetParent(childToken: string): Promise<boolean> {
  const data = await gql('device-management', CLEAR_ASSET_PARENT, { childToken });
  return data.clearAssetParent;
}

// ── Asset-type property contract versions ─────────────────────────────────
//
// An asset type's property schema is a versioned tenant resource: the schema on the
// type is the DRAFT, publishing freezes it, and an asset is validated against the
// type's ACTIVE published version. These four operations are the console's half of
// that — the same draft/publish/rollback shape a device profile and a dashboard have.

export type AssetTypeVersion = AssetTypeVersionsQuery['assetTypeVersions'][number];

const ASSET_TYPE_VERSIONS = graphql(`
  query AssetTypeVersions($token: String!) {
    assetTypeVersions(token: $token) {
      version
      label
      description
      publishedAt
      publishedBy
      propertySchema
    }
  }
`);

export async function listAssetTypeVersions(token: string): Promise<AssetTypeVersion[]> {
  const data = await gql('device-management', ASSET_TYPE_VERSIONS, { token });
  return data.assetTypeVersions;
}

const ACTIVE_ASSET_TYPE_VERSION = graphql(`
  query ActiveAssetTypeVersion($token: String!) {
    activeAssetTypeVersion(token: $token) {
      version
      label
      publishedAt
      propertySchema
    }
  }
`);

// The contract an asset of this type is validated against right now, or null when the
// type has never been published. It is read from its own door rather than picked out
// of the version list by matching AssetType.activeVersion, which would put a second
// implementation of "which one is active" in the client.
export async function getActiveAssetTypeVersion(
  token: string,
): Promise<ActiveAssetTypeVersionQuery['activeAssetTypeVersion']> {
  const data = await gql('device-management', ACTIVE_ASSET_TYPE_VERSION, { token });
  return data.activeAssetTypeVersion;
}

const PUBLISH_ASSET_TYPE = graphql(`
  mutation PublishAssetType($token: String!, $label: String, $description: String) {
    publishAssetType(token: $token, label: $label, description: $description) {
      version
    }
  }
`);

export async function publishAssetType(
  token: string,
  label?: string,
  description?: string,
): Promise<number> {
  const data = await gql('device-management', PUBLISH_ASSET_TYPE, { token, label, description });
  return data.publishAssetType.version;
}

const ROLLBACK_ASSET_TYPE = graphql(`
  mutation RollbackAssetType($token: String!, $version: Int!) {
    rollbackAssetType(token: $token, version: $version) {
      token
      activeVersion
    }
  }
`);

export async function rollbackAssetType(token: string, version: number): Promise<void> {
  await gql('device-management', ROLLBACK_ASSET_TYPE, { token, version });
}
