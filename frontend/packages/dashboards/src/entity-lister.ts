// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// createEntityLister — the device-management-backed EntityCandidateLister a host injects to
// back a ROOT context-selector's candidate set (ADR-039 selection amendment). It lives here,
// beside createDeviceResolver, because this is the package's one seam onto device-management's
// schema; the widget/renderer layers stay presentational and both apps (console + the /dash
// reference viewer) share ONE implementation rather than hand-authoring the same query twice.
//
// Results are memoized per kind for the lister's lifetime: a context-selector re-opens over a
// viewing session, and the tenant's customers/areas are stable enough that a per-mount refetch
// buys nothing. A failed load drops the cache entry so the next open retries.

import { gql } from '@devicechain/client';

import { LIST_AREAS, LIST_ASSETS, LIST_CUSTOMERS, LIST_DEVICES } from './queries';
import type { EntityCandidateLister, EntityListKind } from './types';

// A generous single page — a flat picker over tens of entities, filtered client-side.
const LIST_PAGE_SIZE = 500;

export function createEntityLister(): EntityCandidateLister {
  const cache = new Map<EntityListKind, Promise<Array<{ token: string; name: string | null }>>>();
  const criteria = { pageNumber: 1, pageSize: LIST_PAGE_SIZE };

  return (kind: EntityListKind) => {
    let pending = cache.get(kind);
    if (!pending) {
      pending = fetchKind(kind, criteria).catch((err) => {
        cache.delete(kind);
        throw err;
      });
      cache.set(kind, pending);
    }
    return pending;
  };
}

function fetchKind(
  kind: EntityListKind,
  criteria: { pageNumber: number; pageSize: number },
): Promise<Array<{ token: string; name: string | null }>> {
  switch (kind) {
    case 'device':
      return gql('device-management', LIST_DEVICES, { criteria }).then((r) => r.devices.results);
    case 'customer':
      return gql('device-management', LIST_CUSTOMERS, { criteria }).then((r) => r.customers.results);
    case 'area':
      return gql('device-management', LIST_AREAS, { criteria }).then((r) => r.areas.results);
    case 'asset':
      return gql('device-management', LIST_ASSETS, { criteria }).then((r) => r.assets.results);
    default:
      // An out-of-union kind (a hand-edited definition can carry an empty/other targetType)
      // yields no candidates rather than throwing synchronously inside the lister.
      return Promise.resolve([]);
  }
}
