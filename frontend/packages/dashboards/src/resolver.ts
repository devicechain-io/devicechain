// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The concrete DeviceResolver — the device-management-backed implementation of the
// interface DashboardHub injects. It is the one place the dashboard runtime couples
// to device-management's schema (the Hub itself couples to event-management's
// measurementStream), so the widget/renderer layers above stay purely presentational.
//
//   devicesForAnchor — a CUSTOMER anchor reads customerDevices(customer, relationship); an area
//                      or asset anchor reads anchorDevices(targetType, target), each row being a
//                      member device (the Hub opens one measurementStream per token, per ADR-044).
//
// 🔑 ONE PATH PER ANCHOR KIND, WHOEVER IS VIEWING. The relationship graph is closed to a member
// limited to some customers (a customer user), so an anchor read through it showed that member
// nothing. customerDevices and anchorDevices answer that member about the devices it can see, and
// for every other viewer they answer exactly what the relationship search does, so there is no
// viewer-dependent branch here beyond one: anchorDevices refuses a relationship type from a scoped
// caller, which readAreaOrAssetPage retries without. An anchor the platform still declines
// (FORBIDDEN, reason SCOPE_UNSUPPORTED) is reported by the hub as an unsupported binding
// (isDatasourceSupported) rather than as empty.
//
// Results are memoized per-anchor for the resolver's lifetime: a dashboard resolves
// the same handful of anchors repeatedly (re-mounts, re-renders), and the membership
// is stable for a viewing session.

import { gql, isScopeUnsupportedError } from '@devicechain/client';

import type { DeviceResolver } from './hub';
import type { MemberRef } from './members';
import {
  ANCHOR_DEVICES,
  CUSTOMER_DEVICES,
  DECLARED_METRICS,
  DEVICES_BY_TOKEN,
  DEVICE_NAMES,
  type AnchorDevicesResult,
  type CustomerDevicesResult,
  type DeclaredMetricsResult,
  type DeviceNamesResult,
  type DevicesByTokenResult,
} from './queries';
import type { AnchorTarget } from './types';

// A generous page size for anchor membership — Phase 1 dashboards anchor to areas
// with tens of devices, not thousands; server-side aggregation is Phase 2.
export const ANCHOR_PAGE_SIZE = 500;

// One page of an anchor's members: their tokens, the anchor's true size, their names and their
// device type tokens (both anchor reads carry them, so the roster needs no second read).
interface AnchorPage {
  tokens: string[];
  total: number;
  names: Map<string, string | null>;
  types: Map<string, string | null>;
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
  return readAreaOrAssetPage(anchor);
}

// An area or an asset: anchorDevices, which answers every viewer. The relationship type is sent
// first, so an unscoped viewer gets exactly the devices linked by that type, as the relationship
// search it replaces did. A member limited to some customers is refused the argument (it would let
// them probe which edge types link their devices) and is answered the tracked ownership edges
// instead, so that refusal is retried once without it. A second SCOPE_UNSUPPORTED is the platform
// declining the anchor outright and surfaces as it always has.
function readAreaOrAssetPage(anchor: AnchorTarget): Promise<AnchorPage> {
  const read = (relationshipType?: string) =>
    gql('device-management', ANCHOR_DEVICES, {
      criteria: {
        pageNumber: 1,
        pageSize: ANCHOR_PAGE_SIZE,
        targetType: anchor.targetType,
        target: anchor.targetToken,
        relationshipType,
      },
    });
  return read(anchor.relationship)
    .catch((err: unknown) => {
      if (!isScopeUnsupportedError(err)) throw err;
      return read();
    })
    .then((r: AnchorDevicesResult) => {
      const rows = r.anchorDevices.results;
      return {
        tokens: rows.map((d) => d.token),
        total: trueSize(r.anchorDevices.pagination?.totalRecords, rows.length),
        names: new Map(rows.map((d) => [d.token, d.name])),
        types: new Map(rows.map((d) => [d.token, d.deviceType?.token ?? null])),
      };
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

    // The members of an anchor with their names and the anchor's true size: ONE page, shared with
    // devicesForAnchor, which carries every member's name and device type.
    anchorMembers(anchor: AnchorTarget): Promise<{ members: MemberRef[]; total: number }> {
      const key = anchorKey(anchor);
      let pending = memberCache.get(key);
      if (!pending) {
        pending = anchorPage(anchor)
          .then(({ tokens, total, names, types }) => ({
            members: tokens.map((token) => ({ token, name: names.get(token) ?? null, deviceType: types.get(token) ?? null })),
            total,
          }))
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
