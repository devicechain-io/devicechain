// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Context deep links: a URL that opens a board with a device already bound.
//
//   …/dashboards/<board>?bind.<slot>=<deviceToken>        (console)
//   /dash/?dashboard=<board>&bind.<slot>=<deviceToken>     (the viewer app)
//
// One query parameter per slot, named for the slot, valued with the device token. The slot name
// travels in the KEY (not in a `bind=slot:token` pair) so it needs no escaping rule of its own
// and a second slot is simply a second parameter.
//
// This file is the pure half: reading the parameters against a definition, writing them back,
// and asking the resolver whether the caller can see each device. The React half, which keeps
// the address and the selection in step, is useBoundDevices in @devicechain/widgets.
//
// 🔴 A LINK IS A CLAIM, NOT A GRANT. A token in a URL is whatever its sender typed, so every
// device a link names is checked with the CALLER'S OWN credentials before it is bound. The
// server answers a device outside the caller's tenant or customer scope exactly as it answers a
// device that does not exist (it must not confirm that a row the caller may not see exists), so
// "no such device" and "not yours" are ONE outcome here, never two that could be told apart.

import { isValidToken } from '@devicechain/client';

import type { DeviceResolver } from './hub';
import type { DashboardDefinition, SlotBinding } from './types';

// The query-parameter prefix a bound slot travels under.
export const BIND_PARAM_PREFIX = 'bind.';

// Why a parameter was set aside without being asked of the server.
export type BindParamIssue =
  // `bind.x` where the board has no slot `x` (a link for another board, or a renamed slot).
  | { kind: 'unknown-slot'; slot: string; token: string }
  // `bind.x` where slot `x` is an anchor slot: a link binds a single device.
  | { kind: 'not-a-device-slot'; slot: string; token: string }
  // The value is not a token at all.
  | { kind: 'invalid-token'; slot: string; token: string }
  // `bind.x` given more than once. Which one the link "means" depends on which parser reads it
  // (first wins in one, last in another), so the link is ambiguous and neither is asked of the
  // server. `token` is the first value.
  | { kind: 'duplicate-slot'; slot: string; token: string }
  // `bind.x` where slot `x` follows its parent (`scope.strategy: 'first'`): the board derives
  // that slot's device and ignores any pick, so honouring the link would mean showing a device
  // other than the one in the address.
  | { kind: 'derived-slot'; slot: string; token: string };

export interface ParsedBinds {
  // slot → device token, for the parameters that name a real device slot with a well-formed token.
  binds: Record<string, string>;
  // Everything else a `bind.` parameter could have been, so the viewer is told rather than
  // shown a board that silently ignored half the link.
  issues: BindParamIssue[];
}

function ownSlot(definition: DashboardDefinition, slot: string) {
  const slots = definition.slots;
  return slots && Object.prototype.hasOwnProperty.call(slots, slot) ? slots[slot] : undefined;
}

// parseBindParams reads the `bind.<slot>` parameters of a query string against a board. Pure and
// synchronous: it checks SHAPE only. Whether the device exists, and is the caller's to see, is
// validateBinds.
export function parseBindParams(search: string, definition: DashboardDefinition): ParsedBinds {
  const binds: Record<string, string> = {};
  const issues: BindParamIssue[] = [];
  const params = new URLSearchParams(search);
  const seen = new Set<string>();
  for (const [key, token] of params) {
    if (!key.startsWith(BIND_PARAM_PREFIX)) continue;
    const slot = key.slice(BIND_PARAM_PREFIX.length);
    // Every occurrence after the first is the same ambiguity, reported once.
    if (seen.has(slot)) continue;
    seen.add(slot);
    const def = ownSlot(definition, slot);
    if (params.getAll(key).length > 1) issues.push({ kind: 'duplicate-slot', slot, token });
    else if (!def) issues.push({ kind: 'unknown-slot', slot, token });
    else if (def.type !== 'device') issues.push({ kind: 'not-a-device-slot', slot, token });
    else if (def.scope?.strategy === 'first') issues.push({ kind: 'derived-slot', slot, token });
    else if (!isValidToken(token)) issues.push({ kind: 'invalid-token', slot, token });
    else Object.defineProperty(binds, slot, { value: token, enumerable: true, writable: true, configurable: true });
  }
  return { binds, issues };
}

// The outcome of asking the server about each named device.
export interface BindCheck {
  // Devices the caller can see: safe to bind.
  ok: Record<string, string>;
  // Absent for this caller: deleted, never existed, or outside their tenant or customer scope.
  // Deliberately one bucket.
  missing: Array<{ slot: string; token: string }>;
  // The check itself failed (network, server error). Inconclusive, NOT "missing": retryable.
  failed: Array<{ slot: string; token: string }>;
}

// validateBinds asks the resolver (device-management, under the caller's own token) whether
// each named device is visible. It never throws: a failure is reported per device as `failed`.
export async function validateBinds(binds: Record<string, string>, resolver: Pick<DeviceResolver, 'deviceExists'>): Promise<BindCheck> {
  const out: BindCheck = { ok: {}, missing: [], failed: [] };
  await Promise.all(
    Object.entries(binds).map(async ([slot, token]) => {
      try {
        if (await resolver.deviceExists(token)) Object.defineProperty(out.ok, slot, { value: token, enumerable: true, writable: true, configurable: true });
        else out.missing.push({ slot, token });
      } catch {
        out.failed.push({ slot, token });
      }
    }),
  );
  return out;
}

// bindsToSelection turns validated binds into the selection overlay the hosts hold.
export function bindsToSelection(binds: Record<string, string>): Record<string, SlotBinding> {
  const out: Record<string, SlotBinding> = {};
  for (const [slot, deviceToken] of Object.entries(binds)) {
    Object.defineProperty(out, slot, {
      value: { kind: 'device', deviceToken } satisfies SlotBinding,
      enumerable: true,
      writable: true,
      configurable: true,
    });
  }
  return out;
}

// selectionToBinds is the other direction: the device picks in a selection overlay, for slots
// the board declares as device slots. An anchor pick is not carried (a link binds a device).
export function selectionToBinds(
  selection: Record<string, SlotBinding>,
  definition: DashboardDefinition,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const slot of Object.keys(selection)) {
    const b = selection[slot];
    if (b.kind !== 'device' || ownSlot(definition, slot)?.type !== 'device') continue;
    Object.defineProperty(out, slot, { value: b.deviceToken, enumerable: true, writable: true, configurable: true });
  }
  return out;
}

// withBindParams returns `search` with its `bind.*` parameters replaced by `binds`, every other
// parameter (kiosk, theme, dashboard, …) untouched and in place. Returns the query WITHOUT a
// leading `?`, so a host puts it wherever its router wants it; empty when nothing is left.
export function withBindParams(search: string, binds: Record<string, string>): string {
  const next = new URLSearchParams();
  for (const [k, v] of new URLSearchParams(search)) if (!k.startsWith(BIND_PARAM_PREFIX)) next.append(k, v);
  for (const slot of Object.keys(binds).sort()) next.append(BIND_PARAM_PREFIX + slot, binds[slot]);
  return next.toString();
}

// bindParamsOf is the `bind.*` slice of a query string, normalised (sorted, no `?`), so two
// addresses can be compared for "same link" regardless of parameter order.
export function bindParamsOf(search: string): string {
  const only = new URLSearchParams();
  for (const [k, v] of new URLSearchParams(search)) if (k.startsWith(BIND_PARAM_PREFIX)) only.append(k, v);
  only.sort();
  return only.toString();
}
