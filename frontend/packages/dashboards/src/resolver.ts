// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The concrete DeviceResolver — the device-management-backed implementation of the
// interface DashboardHub injects. It is the one place the dashboard runtime couples
// to device-management's schema (the Hub itself couples to event-management's
// measurementStream), so the widget/renderer layers above stay purely presentational.
//
//   devicesForAnchor — a CUSTOMER anchor reads customerDevices(customer, relationship);
//                      an area or asset anchor reads entityRelationships filtered to
//                      (source=device, target=anchor), each relationship's source token
//                      being a member device (the Hub opens one measurementStream per
//                      token, per ADR-044).
//
// 🔑 ONE PATH PER ANCHOR KIND, WHOEVER IS VIEWING. The relationship graph is closed to a
// member limited to some customers (a customer user), so a customer anchor read through it
// showed that member nothing; customerDevices answers them about their own customers, and
// for every other viewer it answers exactly what the relationship search does (the
// device-management test TestCustomerDevicesMatchesTheRelationshipSearchForAnUnscopedCaller
// pins that), so there is no viewer-dependent branch here. Area and asset anchors stay on
// the graph: a customer user is refused them (FORBIDDEN, reason SCOPE_UNSUPPORTED), and the
// hub reports that as an unsupported binding (isDatasourceSupported) rather than as empty.
//
// Results are memoized per-anchor for the resolver's lifetime: a dashboard resolves
// the same handful of anchors repeatedly (re-mounts, re-renders), and the membership
// is stable for a viewing session.

import { gql } from '@devicechain/client';

import type { DeviceResolver } from './hub';
import type { MemberRef } from './members';
import {
  CUSTOMER_DEVICES,
  DECLARED_METRICS,
  DEVICES_BY_TOKEN,
  DEVICES_FOR_ANCHOR,
  DEVICE_NAMES,
  type CustomerDevicesResult,
  type DeclaredMetricsResult,
  type DeviceNamesResult,
  type DevicesByTokenResult,
  type EntityRelationshipsResult,
} from './queries';
import type { AnchorTarget } from './types';

// A generous page size for anchor membership — Phase 1 dashboards anchor to areas
// with tens of devices, not thousands; server-side aggregation is Phase 2.
export const ANCHOR_PAGE_SIZE = 500;

// One page of an anchor's members: their tokens, the anchor's true size, and their names when
// the read that listed them carried them (customerDevices does; the relationship search does not).
interface AnchorPage {
  tokens: string[];
  total: number;
  names?: Map<string, string | null>;
  // Their device type tokens, from the same read as `names`.
  types?: Map<string, string | null>;
}

// The most device types one declaration read asks for.
const DECLARED_BATCH_SIZE = 100;

// A server that reports no total is taken at its word: the page is the fleet.
const trueSize = (reported: number | null | undefined, page: number) => Math.max(reported ?? page, page);

function readAnchorPage(anchor: AnchorTarget): Promise<AnchorPage> {
  if (anchor.targetType === 'customer') {
    return gql('device-management', CUSTOMER_DEVICES, {
      criteria: {
        pageNumber: 1,
        pageSize: ANCHOR_PAGE_SIZE,
        customer: anchor.targetToken,
        relationshipType: anchor.relationship,
      },
    }).then((r: CustomerDevicesResult) => {
      const rows = r.customerDevices.results;
      return {
        tokens: rows.map((d) => d.token),
        total: trueSize(r.customerDevices.pagination?.totalRecords, rows.length),
        names: new Map(rows.map((d) => [d.token, d.name])),
        types: new Map(rows.map((d) => [d.token, d.deviceType?.token ?? null])),
      };
    });
  }
  return gql('device-management', DEVICES_FOR_ANCHOR, {
    criteria: {
      pageNumber: 1,
      pageSize: ANCHOR_PAGE_SIZE,
      sourceType: 'device',
      targetType: anchor.targetType,
      target: anchor.targetToken,
      relationshipType: anchor.relationship,
    },
  }).then((r: EntityRelationshipsResult) => {
    const tokens = r.entityRelationships.results.map((rel) => rel.source.token);
    return { tokens, total: trueSize(r.entityRelationships.pagination?.totalRecords, tokens.length) };
  });
}

export function createDeviceResolver(): DeviceResolver {
  const anchorCache = new Map<string, Promise<AnchorPage>>();
  const memberCache = new Map<string, Promise<{ members: MemberRef[]; total: number }>>();
  // Memoize existence per token for the resolver's lifetime: many widgets on a
  // dashboard bind the same device, and a device's existence is stable for a viewing
  // session (a delete mid-session is rare and the next mount re-checks).
  const existsCache = new Map<string, Promise<boolean>>();
  const typeCache = new Map<string, Promise<string | null>>();

  // The device types asked for declarations in the current tick, and who is waiting on each.
  type DeclaredWaiter = { resolve: (names: string[] | null) => void; reject: (err: unknown) => void };
  let declaredBatch: Map<string, DeclaredWaiter[]> | null = null;

  function anchorKey(anchor: AnchorTarget): string {
    return `${anchor.relationship}|${anchor.targetType}|${anchor.targetToken}`;
  }

  // One page of the anchor's members and the anchor's true size, memoized per anchor.
  function anchorPage(anchor: AnchorTarget): Promise<AnchorPage> {
    const key = anchorKey(anchor);
    let pending = anchorCache.get(key);
    if (!pending) {
      pending = readAnchorPage(anchor)
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

    // The members of an anchor with their names and the anchor's true size. One page (shared with
    // devicesForAnchor) plus, when that page carried no names, ONE batched name read, whatever the
    // size of the fleet. A failed name read is not a failed roster: the tokens still stream, and
    // the widget labels them by token.
    anchorMembers(anchor: AnchorTarget): Promise<{ members: MemberRef[]; total: number }> {
      const key = anchorKey(anchor);
      let pending = memberCache.get(key);
      if (!pending) {
        pending = anchorPage(anchor)
          .then(async ({ tokens, total, names: listed, types: listedTypes }) => {
            const names = listed ?? new Map<string, string | null>();
            const types = listedTypes ?? new Map<string, string | null>();
            if (!listed && tokens.length > 0) {
              try {
                const r: DeviceNamesResult = await gql('device-management', DEVICE_NAMES, { tokens });
                for (const d of r.devicesByToken) {
                  names.set(d.token, d.name);
                  types.set(d.token, d.deviceType?.token ?? null);
                }
              } catch {
                // labelled by token, and of no known type
              }
            }
            return { members: tokens.map((token) => ({ token, name: names.get(token) ?? null, deviceType: types.get(token) ?? null })), total };
          })
          .catch((err) => {
            memberCache.delete(key);
            throw err;
          });
        memberCache.set(key, pending);
      }
      return pending;
    },

    // The names a device type's active profile version declares, or null when it has nothing to
    // say: no profile, never published, not found, or a published version that declares no metric
    // at all (an undeclared measurement is still accepted, so an empty declaration rules nothing
    // out). A failed read rejects: the caller treats that as unknown too, and does not cache it.
    // Every type asked in the same tick goes in ONE read, so a board of many types costs one
    // request rather than one per type.
    declaredMetrics(deviceType: string): Promise<string[] | null> {
      return new Promise((resolve, reject) => {
        let batch = declaredBatch;
        if (!batch) {
          batch = declaredBatch = new Map();
          queueMicrotask(() => {
            const asked = declaredBatch!;
            declaredBatch = null;
            const tokens = [...asked.keys()];
            for (let i = 0; i < tokens.length; i += DECLARED_BATCH_SIZE) {
              const chunk = tokens.slice(i, i + DECLARED_BATCH_SIZE);
              gql('device-management', DECLARED_METRICS, { tokens: chunk }).then(
                (r: DeclaredMetricsResult) => {
                  for (const token of chunk) {
                    const version = r.deviceTypesByToken.find((t) => t.token === token)?.profile?.activeProfileVersion;
                    const names = version && version.metrics.length > 0 ? version.metrics.map((m) => m.metricKey) : null;
                    for (const w of asked.get(token)!) w.resolve(names);
                  }
                },
                (err: unknown) => {
                  for (const token of chunk) for (const w of asked.get(token)!) w.reject(err);
                },
              );
            }
          });
        }
        const waiting = batch.get(deviceType) ?? [];
        waiting.push({ resolve, reject });
        batch.set(deviceType, waiting);
      });
    },

    // The device type's token, from the same names read the roster uses. A failed read rejects (the
    // caller treats that as unknown) and is not cached.
    deviceType(deviceToken: string): Promise<string | null> {
      let pending = typeCache.get(deviceToken);
      if (!pending) {
        pending = gql('device-management', DEVICE_NAMES, { tokens: [deviceToken] })
          .then((r: DeviceNamesResult) => r.devicesByToken.find((d) => d.token === deviceToken)?.deviceType?.token ?? null)
          .catch((err) => {
            typeCache.delete(deviceToken);
            throw err;
          });
        typeCache.set(deviceToken, pending);
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
