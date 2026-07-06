// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The concrete DeviceResolver — the device-management-backed implementation of the
// interface DashboardHub injects. It is the one place the dashboard runtime couples
// to device-management's schema (the Hub itself couples to event-management's
// measurementStream), so the widget/renderer layers above stay purely presentational.
//
//   devicesForAnchor — entityRelationships filtered to (source=device, target=anchor)
//                      → each relationship's source token is a member device (the Hub
//                      opens one measurementStream per token, per ADR-044).
//
// Results are memoized per-anchor for the resolver's lifetime: a dashboard resolves
// the same handful of anchors repeatedly (re-mounts, re-renders), and the membership
// is stable for a viewing session.

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
  const anchorCache = new Map<string, Promise<string[]>>();
  // Memoize existence per token for the resolver's lifetime: many widgets on a
  // dashboard bind the same device, and a device's existence is stable for a viewing
  // session (a delete mid-session is rare and the next mount re-checks).
  const existsCache = new Map<string, Promise<boolean>>();

  return {
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
            r.entityRelationships.results.map((rel) => rel.source.token),
          )
          .catch((err) => {
            anchorCache.delete(key);
            throw err;
          });
        anchorCache.set(key, pending);
      }
      return pending;
    },

    deviceExists(deviceToken: string): Promise<boolean> {
      let pending = existsCache.get(deviceToken);
      if (!pending) {
        pending = gql('device-management', DEVICES_BY_TOKEN, { tokens: [deviceToken] })
          .then((r: DevicesByTokenResult) => r.devicesByToken.some((d) => d.token === deviceToken))
          .catch((err) => {
            // Don't cache a failure as "gone": drop it so the next check retries, and
            // rethrow so the caller treats an existence-check outage as inconclusive
            // (available) rather than falsely rendering a live device unavailable.
            existsCache.delete(deviceToken);
            throw err;
          });
        existsCache.set(deviceToken, pending);
      }
      return pending;
    },
  };
}
