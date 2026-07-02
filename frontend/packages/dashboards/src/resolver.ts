// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The concrete DeviceResolver — the device-management-backed implementation of the
// interface DashboardHub injects. It is the one place the dashboard runtime couples
// to device-management's schema (the Hub itself couples to event-management's
// measurementStream), so the widget/renderer layers above stay purely presentational.
//
//   deviceIdForToken — devicesByToken → numeric Device.id (measurementStream filters
//                      on the numeric id, not the token).
//   devicesForAnchor — entityRelationships filtered to (source=device, target=anchor)
//                      → each relationship's source id is a member device.
//
// Results are memoized per-token/per-anchor for the resolver's lifetime: a dashboard
// resolves the same handful of devices/anchors repeatedly (re-mounts, re-renders),
// and the membership is stable for a viewing session.

import { gql } from '@devicechain/client';

import type { DeviceResolver } from './hub';
import {
  DEVICES_BY_TOKEN,
  DEVICES_FOR_ANCHOR,
  type DevicesByTokenResult,
  type EntityRelationshipsResult,
} from './queries';
import type { AnchorTarget } from './types';

// A generous page size for anchor membership — Phase 1 dashboards anchor to areas
// with tens of devices, not thousands; server-side aggregation is Phase 2.
const ANCHOR_PAGE_SIZE = 500;

export function createDeviceResolver(): DeviceResolver {
  const tokenCache = new Map<string, Promise<string | null>>();
  const anchorCache = new Map<string, Promise<string[]>>();

  return {
    deviceIdForToken(token: string): Promise<string | null> {
      let pending = tokenCache.get(token);
      if (!pending) {
        pending = gql('device-management', DEVICES_BY_TOKEN, { tokens: [token] })
          .then((r: DevicesByTokenResult) => r.devicesByToken[0]?.id ?? null)
          .catch((err) => {
            tokenCache.delete(token); // don't cache a transient failure
            throw err;
          });
        tokenCache.set(token, pending);
      }
      return pending;
    },

    devicesForAnchor(anchor: AnchorTarget): Promise<string[]> {
      const key = `${anchor.relationship}|${anchor.targetType}|${anchor.targetToken}`;
      let pending = anchorCache.get(key);
      if (!pending) {
        pending = gql('device-management', DEVICES_FOR_ANCHOR, {
          criteria: {
            pageNumber: 1,
            pageSize: ANCHOR_PAGE_SIZE,
            sourceType: 'device',
            targetType: anchor.targetType,
            target: anchor.targetToken,
            relationshipType: anchor.relationship,
          },
        })
          .then((r: EntityRelationshipsResult) =>
            r.entityRelationships.results.map((rel) => rel.source.id),
          )
          .catch((err) => {
            anchorCache.delete(key);
            throw err;
          });
        anchorCache.set(key, pending);
      }
      return pending;
    },
  };
}
