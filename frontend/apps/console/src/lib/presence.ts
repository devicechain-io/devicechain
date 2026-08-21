// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a device's presence projection actually CLAIMS, as four values instead of
// one boolean.
//
// 🔴 WHY THIS EXISTS. `active === false` is two different facts wearing one face:
//
//   - ASSERTED + inactive — a presence-asserting transport (a real-MQTT LWT, a
//     Sparkplug NDEATH) TOLD us the device went away. The device is down. Saying
//     "Disconnected" is a claim we can support.
//   - INFERRED + inactive — nobody told us anything. We have merely not seen data
//     inside the inactivity window, which is exactly what a healthy device on a slow
//     reporting interval looks like. Calling that "Offline" full stop is a claim
//     about the DEVICE when the truth is a claim about our own evidence.
//
// Collapsing the two is what let the console report a device as down on nothing more
// than silence, so the discriminator (ADR-067 decision 3) is read here once and every
// screen asks this function rather than re-deriving it from `active`.
//
// 🔴 UNRECOGNISED SOURCES CLASSIFY AS `quiet`, deliberately. This mirrors
// device-state's own fail-safe — `device-state/model/model.go`: "Any value that is
// not exactly ASSERTED is treated as inferred (fail-safe toward today's behavior)".
// A value this console has never heard of is not evidence that a transport spoke, so
// it must never buy the confident "Disconnected" wording. The natural-looking rewrite
// (`!== 'INFERRED'`) inverts that fail-safe, which is why the test table pins it.

/** The presence-source discriminator values device-state serves. */
const ASSERTED = 'ASSERTED';
const INFERRED = 'INFERRED';

/**
 * The four answers to "what does the platform know about this device's presence?".
 *
 * - `unknown` — no state row at all: no state:read authority, or the device has
 *   never reported. Not a claim about the device, and must not render as one.
 * - `online` — the projection says active, whichever way it got there.
 * - `quiet` — inferred and inactive: silence, not a reported disconnect.
 * - `disconnected` — asserted and inactive: a transport reported it gone.
 */
export type PresenceKind = 'unknown' | 'online' | 'quiet' | 'disconnected';

/** The shape presenceKind reads — a structural subset of the device-state row. */
export interface PresenceFacts {
  active: boolean;
  presenceSource: string;
}

export function presenceKind(state: PresenceFacts | null | undefined): PresenceKind {
  if (!state) return 'unknown';
  if (state.active) return 'online';
  // Exact equality, not a negation of INFERRED: see the fail-safe note above.
  return state.presenceSource === ASSERTED ? 'disconnected' : 'quiet';
}

/**
 * The i18n key wording a presence source for an operator, or null when the row
 * carries a value this console does not know.
 *
 * An unrecognised value is reported VERBATIM by the caller rather than folded into
 * one of the two known labels: the classifier already treats it conservatively, and
 * telling an operator the row says "Inferred" when it says something else would hide
 * exactly the disagreement worth seeing.
 */
export function presenceSourceLabelKey(presenceSource: string): string | null {
  if (presenceSource === ASSERTED) return 'presenceSourceAsserted';
  if (presenceSource === INFERRED) return 'presenceSourceInferred';
  return null;
}
