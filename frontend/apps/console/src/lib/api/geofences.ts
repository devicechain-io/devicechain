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
import type {
  GeoFencesQuery,
  GeoFenceCreateRequest,
  GeoFenceUpdateRequest,
  GeoFenceSetSnapshotQuery,
} from '@/gql/device-management/graphql';

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
  mutation UpdateGeoFence($token: String!, $request: GeoFenceUpdateRequest!) {
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
 * A PARTIAL update: a field this request does not name keeps its stored value, and
 * `geometry` — which a fence cannot be without — refuses an explicit null.
 *
 * 🔴 THE REQUEST CARRIES NO token, and that is the immutability rule rather than a
 * convenience. Detection rules name fences by token inside compiled predicates this
 * service cannot rewrite, so a rename would leave every one of them naming nothing.
 * The server used to refuse a differing payload token; the input now has nowhere to
 * write one, which is the same rule with no guard to go stale.
 */
export async function updateGeoFence(
  token: string,
  request: GeoFenceUpdateRequest,
): Promise<GeoFence> {
  const data = await gql('device-management', UPDATE_GEO_FENCE, { token, request });
  return data.updateGeoFence;
}

// 🔴 geoFencePreserved IS GONE, AND ITS ABSENCE IS THE POINT. It projected everything
// a fence already was so an editor that touched three fields did not erase the fourth —
// a workaround for updateGeoFence being a full replace. The server now leaves what a
// request does not name alone, so re-sending the stored metadata is no longer a
// safeguard: it is a write of a value the caller never looked at, over whatever the
// value has become since the form was opened. Not sending it is strictly safer.

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

// ── The frozen archive ──────────────────────────────────────────────────────
//
// A mutation that changes the fence SET — a fence created, a boundary edited, a
// fence deleted — mints a fence-set version and freezes the geometry of every
// fence into it. Renaming a fence, or editing its description or metadata, mints
// NOTHING: none of them are in the frozen snapshot, so the new version would name
// shapes identical to the current one. Expect the version to sit still after such
// a save; it is not a lost write.
//
// A Location event carries the version it was stamped with, so a rule replayed
// against last week's events is evaluated against last week's fences — which is
// what makes a preview correct rather than a fiction. These two queries are how
// the console shows that archive.

const CURRENT_FENCE_SET_VERSION = graphql(`
  query CurrentFenceSetVersion {
    currentFenceSetVersion
  }
`);

/**
 * The version Location events are currently being stamped with.
 *
 * 🔴 Zero means no set has ever been minted — NOT "there are no fences". The two
 * are indistinguishable from this number alone, which is why the history panel
 * reports the empty case in words rather than showing a version of 0.
 */
export async function getCurrentFenceSetVersion(): Promise<number> {
  const data = await gql('device-management', CURRENT_FENCE_SET_VERSION, undefined);
  return data.currentFenceSetVersion;
}

const GEO_FENCE_SET_SNAPSHOT = graphql(`
  query GeoFenceSetSnapshot($version: Int!, $pagination: PaginationInput!) {
    geoFenceSetSnapshot(version: $version) {
      version
      fences(pagination: $pagination) {
        results {
          token
          geometry
        }
        pagination {
          totalRecords
        }
      }
    }
  }
`);

/**
 * The page size this panel reads a frozen set in.
 *
 * A fence set cannot hold more than a hundred fences — the server refuses the
 * hundred-and-first — so one page of that size is the whole set for every fence
 * set that can exist today. The loop below runs anyway, because "the limit is a
 * hundred" is the server's decision to change and a panel that silently showed
 * the first page of a raised limit would be wrong without ever looking wrong.
 */
const SNAPSHOT_PAGE_SIZE = 100;

/**
 * The most pages this loop will ask for before giving up.
 *
 * It runs inside a render path, so an unbounded loop here is a hung tab rather
 * than a slow one. A hundred fences at a page size of a hundred is one request,
 * so eight is several times anything reachable — a runaway stop, not a second
 * limit on how many fences a set may hold.
 */
const SNAPSHOT_MAX_PAGES = 8;

export type GeoFenceSetSnapshotFence = NonNullable<
  GeoFenceSetSnapshotQuery['geoFenceSetSnapshot']
>['fences']['results'][number];

/** A whole frozen fence set, reassembled from however many pages it took. */
export interface GeoFenceSetSnapshot {
  version: number;
  fences: GeoFenceSetSnapshotFence[];
}

/**
 * The whole fence set as it stood at `version`, geometry frozen.
 *
 * The entries carry token and geometry only: a fence's NAME is deliberately not
 * frozen, so this cannot answer "what was it called then" and the panel must not
 * imply that it can.
 *
 * The fences arrive PAGED. That is not for this panel's benefit — a hundred
 * fences is a small render — it is because the same field is read across a
 * service boundary by a client that caps a response at a megabyte, and a fence
 * set at the documented authoring limits is larger than that. This function
 * hides the paging so the panel keeps working with a whole set.
 */
export async function getGeoFenceSetSnapshot(version: number): Promise<GeoFenceSetSnapshot> {
  const fences: GeoFenceSetSnapshotFence[] = [];
  let resolved = version;
  for (let pageNumber = 1; pageNumber <= SNAPSHOT_MAX_PAGES; pageNumber++) {
    const data = await gql('device-management', GEO_FENCE_SET_SNAPSHOT, {
      version,
      pagination: { pageNumber, pageSize: SNAPSHOT_PAGE_SIZE },
    });
    const snapshot = data.geoFenceSetSnapshot;
    resolved = snapshot.version;
    fences.push(...snapshot.fences.results);

    // 🔴 A MISSING TOTAL IS AN ERROR, NOT A ZERO. `?? 0` would end the loop after
    // the first page and hand back whatever arrived — and a truncated fence set
    // is indistinguishable from a small one, so the panel would draw a confident
    // picture of a set that is not the set. The server-side reader refuses the
    // same wire condition for the same reason; two readers of one field must not
    // disagree about what a null there means.
    const total = snapshot.fences.pagination.totalRecords;
    if (total == null) {
      throw new Error(
        'The fence set came back without a record count, so there is no way to tell a ' +
          'complete set from a truncated one.',
      );
    }
    if (fences.length >= total) return { version: resolved, fences };
    // A page that returns nothing while the total says otherwise cannot be
    // completed by asking again.
    if (snapshot.fences.results.length === 0) {
      throw new Error(
        `The fence set reported ${total} fences but stopped answering after ${fences.length}.`,
      );
    }
  }
  throw new Error(
    `The fence set did not finish loading within ${SNAPSHOT_MAX_PAGES} pages.`,
  );
}
