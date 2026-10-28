// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which sidebar sections the user has expanded or collapsed, remembered per browser. A pure
// convenience: storage can be absent, full, blocked or hold something another version wrote, and
// none of that may change what the sidebar renders beyond the remembered state, so every access is
// guarded and the result is validated rather than trusted.

export const NAV_STATE_KEY = 'dc.console.nav';

export type NavOpenState = Record<string, boolean>;

/** The remembered state, or `{}` when there is none, it is unreadable, or it is not a record of booleans. */
export function readNavState(): NavOpenState {
  try {
    const raw = window.localStorage.getItem(NAV_STATE_KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return {};
    const out: NavOpenState = {};
    for (const [k, v] of Object.entries(parsed)) if (typeof v === 'boolean') out[k] = v;
    return out;
  } catch {
    return {};
  }
}

/** Best-effort: a failed write leaves the in-memory state correct for this page view. */
export function writeNavState(state: NavOpenState): void {
  try {
    window.localStorage.setItem(NAV_STATE_KEY, JSON.stringify(state));
  } catch {
    // Storage unavailable (private mode, quota, blocked): the choice simply is not remembered.
  }
}
