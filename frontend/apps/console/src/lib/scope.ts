// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the console offers a member whose access is limited to part of the tenant (a
// customer user). The server is the enforcement point — every operation not opted in
// answers FORBIDDEN, and a device outside the scope answers NOT_FOUND — so nothing here
// grants anything. It only stops the console from offering pages and actions the server
// would refuse, which a scoped user would otherwise meet as a wall of errors.
import { hasAuthority, isScoped, type DecodedClaims } from '@devicechain/client';

// The routes a scoped member may open, as path patterns over the console's route table
// (App.tsx). Listed from the ROUTE TABLE rather than from the nav, because a deep link or
// a bookmark reaches a route the nav never offered. Each one renders only reads the server
// admits for a scoped caller: devices, their live state, events and commands, alarms, and
// the published dashboards shared with the member's customers.
//
// 🔴 A route added to App.tsx is NOT here, so a scoped member is sent home from it. That is
// the safe default: a new page has to be shown to work for a scoped caller before it is
// listed, the same opt-in the server applies to a new resolver.
export const SCOPED_ROUTES: readonly RegExp[] = [
  /^\/$/,
  /^\/devices$/,
  /^\/devices\/[^/]+$/,
  /^\/alarms$/,
  /^\/dashboards$/,
  /^\/dashboards\/[^/]+$/,
];

/** True when a scoped member may open the path. */
export function scopedRouteAllowed(pathname: string): boolean {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname;
  return SCOPED_ROUTES.some((r) => r.test(path));
}

/**
 * True when the caller may CHANGE things guarded by the authority: they hold it and are
 * not limited to part of the tenant. Writes other than acknowledging alarms and sending
 * commands are refused to a scoped member whatever authorities their role names, so an
 * editor offered to one would fail on every save.
 */
export function mayWrite(claims: DecodedClaims | null, authority: string): boolean {
  return hasAuthority(claims, authority) && !isScoped(claims);
}
