// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the kiosk playlist (Playlist.tsx is the clock) (`?rotate=tokA,tokB&every=60`): which board is next, and the
// link a board is given. The clock, the preload and the controls are Playlist.tsx.

import { BIND_PARAM_PREFIX, type DashboardDefinition } from '@devicechain/dashboards';

import type { LoadError } from './load';

// How often the progress clock ticks, and how long after the viewer last touched the board the
// rotation waits before it moves on again.
export const TICK_MS = 250;
export const IDLE_RESUME_MS = 30_000;
// A board is read this long before it is shown (or at once, for a short interval), so the switch
// is to a board that is already here, yet a long interval never shows a definition an hour old.
export const PRELOAD_LEAD_MS = 10_000;

// The index `step` boards from `from`, wrapping around.
export function stepIndex(from: number, step: 1 | -1, count: number): number {
  return (((from + step) % count) + count) % count;
}

// The link a board is opened with: every parameter the link carries, with each `bind.<slot>` kept
// only on a board that HAS that slot. One link binds a device for the boards that take it; a board
// with no such slot is shown as it is, rather than refused for a slot meant for another board.
export function boardAddress(search: string, definition: Pick<DashboardDefinition, 'slots'>): string {
  const slots = definition.slots ?? {};
  const kept = new URLSearchParams();
  for (const [key, value] of new URLSearchParams(search)) {
    if (key.startsWith(BIND_PARAM_PREFIX) && !Object.prototype.hasOwnProperty.call(slots, key.slice(BIND_PARAM_PREFIX.length))) continue;
    kept.append(key, value);
  }
  const text = kept.toString();
  return text === '' ? '' : `?${text}`;
}

// How far along the board on screen is, 0 to 1.
export function progressOf(elapsedMs: number, everySeconds: number): number {
  return Math.min(1, Math.max(0, elapsedMs / (everySeconds * 1000)));
}

// What a skipped board's note carries: the token and why, as a code, never as words.
export interface Skipped {
  token: string;
  error: LoadError;
}
