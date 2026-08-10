// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for geofences, served by device-management alongside
// the device/asset/customer/area registries.
//
// Two things about this API are unlike the other registry families and both
// shape the UI:
//
//   1. `geometry` is an opaque JSON STRING on the wire, not a structured type.
//      The server stores the document verbatim, so the console owns its shape.
//      Building and reading it lives in routes/geofences/geometry.ts.
//
//   2. Every mutation — create, update, DELETE, and even a name-only edit —
//      mints a new fence-set version as a side effect, in the same transaction.
//      There is no publish step to call and none to offer: the version is the
//      mutation's consequence, not a separate action.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { GeoFencesQuery, GeoFenceCreateRequest } from '@/gql/device-management/graphql';

// Derived from the generated operation result so it can never drift from the
// selection set actually sent.
export type GeoFence = GeoFencesQuery['geoFences']['results'][number];
export type GeoFenceSearchResults = GeoFencesQuery['geoFences'];

export type { GeoFenceCreateRequest };

const GEO_FENCES = graphql(`
  query GeoFences($criteria: GeoFenceSearchCriteria!) {
    geoFences(criteria: $criteria) {
      results {
        id
        token
        name
        description
        geometry
        kind
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

export async function listGeoFences(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<GeoFenceSearchResults> {
  const data = await gql('device-management', GEO_FENCES, {
    criteria: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  return data.geoFences;
}

// The getter and both mutations select the same shape as the list query so every
// result stays assignable to the shared GeoFence type.
const GEO_FENCE_BY_TOKEN = graphql(`
  query GeoFenceByToken($tokens: [String!]!) {
    geoFencesByToken(tokens: $tokens) {
      id
      token
      name
      description
      geometry
      kind
      metadata
      createdAt
    }
  }
`);

export async function getGeoFence(token: string): Promise<GeoFence | null> {
  const data = await gql('device-management', GEO_FENCE_BY_TOKEN, { tokens: [token] });
  return data.geoFencesByToken[0] ?? null;
}

const CREATE_GEO_FENCE = graphql(`
  mutation CreateGeoFence($request: GeoFenceCreateRequest!) {
    createGeoFence(request: $request) {
      id
      token
      name
      description
      geometry
      kind
      metadata
      createdAt
    }
  }
`);

export async function createGeoFence(request: GeoFenceCreateRequest): Promise<GeoFence> {
  const data = await gql('device-management', CREATE_GEO_FENCE, { request });
  return data.createGeoFence;
}

const UPDATE_GEO_FENCE = graphql(`
  mutation UpdateGeoFence($token: String!, $request: GeoFenceCreateRequest!) {
    updateGeoFence(token: $token, request: $request) {
      id
      token
      name
      description
      geometry
      kind
      metadata
      createdAt
    }
  }
`);

/**
 * 🔴 A FULL REPLACE. `request` carries every field, so a caller that builds it
 * from partial form state blanks whatever it left out — the geometry included.
 * One form state, one save.
 *
 * The `token` argument is the EXISTING token; `request.token` is the one to
 * store, so a rename is expressible. The console does not offer one (the token
 * field is fixed on edit, as it is for every other registry family), but the
 * asymmetry is the API's, not a mistake here.
 */
export async function updateGeoFence(
  token: string,
  request: GeoFenceCreateRequest,
): Promise<GeoFence> {
  const data = await gql('device-management', UPDATE_GEO_FENCE, { token, request });
  return data.updateGeoFence;
}

const DELETE_GEO_FENCE = graphql(`
  mutation DeleteGeoFence($token: String!) {
    deleteGeoFence(token: $token)
  }
`);

/**
 * 🔴 A HARD delete — the row is removed, not soft-deleted, so the token becomes
 * reusable immediately. What survives is the geometry inside every already-minted
 * fence-set version, which is what keeps historical detections explicable.
 */
export async function deleteGeoFence(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_GEO_FENCE, { token });
  return data.deleteGeoFence;
}
