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
import type { MemberRef } from './members';
import {
  DEVICES_BY_TOKEN,
  DEVICES_FOR_ANCHOR,
  DEVICE_NAMES,
  type DeviceNamesResult,
  type DevicesByTokenResult,
  type EntityRelationshipsResult,
} from './queries';
import type { AnchorTarget } from './types';

// A generous page size for anchor membership — Phase 1 dashboards anchor to areas
// with tens of devices, not thousands; server-side aggregation is Phase 2.
export const ANCHOR_PAGE_SIZE = 500;

export function createDeviceResolver(): DeviceResolver {
  const anchorCache = new Map<string, Promise<{ tokens: string[]; total: number }>>();
  const memberCache = new Map<string, Promise<{ members: MemberRef[]; total: number }>>();
  // Memoize existence per token for the resolver's lifetime: many widgets on a
  // dashboard bind the same device, and a device's existence is stable for a viewing
  // session (a delete mid-session is rare and the next mount re-checks).
  const existsCache = new Map<string, Promise<boolean>>();

  function anchorKey(anchor: AnchorTarget): string {
    return `${anchor.relationship}|${anchor.targetType}|${anchor.targetToken}`;
  }

  // One page of the anchor's members and the anchor's true size, memoized per anchor.
  function anchorPage(anchor: AnchorTarget): Promise<{ tokens: string[]; total: number }> {
    const key = anchorKey(anchor);
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
        .then((r: EntityRelationshipsResult) => {
          const tokens = r.entityRelationships.results.map((rel) => rel.source.token);
          // A server that reports no total is taken at its word: the page is the fleet.
          const total = r.entityRelationships.pagination?.totalRecords ?? tokens.length;
          return { tokens, total: Math.max(total, tokens.length) };
        })
        .catch((err) => {
          anchorCache.delete(key);
          throw err;
        });
      anchorCache.set(key, pending);
    }
    return pending;
  }

  return {
    devicesForAnchor(anchor: AnchorTarget): Promise<string[]> {
      return anchorPage(anchor).then((page) => page.tokens);
    },

    // The members of an anchor with their names and the anchor's true size. One relationship page
    // (shared with devicesForAnchor) plus ONE batched name read, whatever the size of the fleet. A
    // failed name read is not a failed roster: the tokens still stream, and the widget labels
    // them by token.
    anchorMembers(anchor: AnchorTarget): Promise<{ members: MemberRef[]; total: number }> {
      const key = anchorKey(anchor);
      let pending = memberCache.get(key);
      if (!pending) {
        pending = anchorPage(anchor)
          .then(async ({ tokens, total }) => {
            const names = new Map<string, string | null>();
            if (tokens.length > 0) {
              try {
                const r: DeviceNamesResult = await gql('device-management', DEVICE_NAMES, { tokens });
                for (const d of r.devicesByToken) names.set(d.token, d.name);
              } catch {
                // labelled by token
              }
            }
            return { members: tokens.map((token) => ({ token, name: names.get(token) ?? null })), total };
          })
          .catch((err) => {
            memberCache.delete(key);
            throw err;
          });
        memberCache.set(key, pending);
      }
      return pending;
    },

    // deviceExists caches only a POSITIVE result (a live device is stable for the
    // session); a negative and an error both drop the entry so a later check re-queries.
    // ADR-042 frees a token on delete, so a deleted-then-recreated device must be able to
    // recover on a long-lived viewer (the availability hook re-checks on a timer while a
    // widget shows unavailable) rather than staying stuck "gone" for the whole session.
    // The in-flight promise is still shared, so concurrent checks for one token coalesce.
    // (Batching distinct tokens into one devicesByToken call is a deferred optimization —
    // Phase-1 dashboards bind a handful of devices.)
    deviceExists(deviceToken: string): Promise<boolean> {
      let pending = existsCache.get(deviceToken);
      if (!pending) {
        pending = gql('device-management', DEVICES_BY_TOKEN, { tokens: [deviceToken] })
          .then((r: DevicesByTokenResult) => {
            const exists = r.devicesByToken.some((d) => d.token === deviceToken);
            if (!exists) existsCache.delete(deviceToken);
            return exists;
          })
          .catch((err) => {
            // A failure is inconclusive, not "gone": drop the entry so the next check
            // retries, and rethrow so the caller fails open (renders available).
            existsCache.delete(deviceToken);
            throw err;
          });
        existsCache.set(deviceToken, pending);
      }
      return pending;
    },
  };
}
