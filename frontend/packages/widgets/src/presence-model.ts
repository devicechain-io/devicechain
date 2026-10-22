// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the device status card and the online doughnut say, decided apart from the drawing. Pure, so
// each rule is a test.
//
//   - a device is ONLINE when device-state considers it connected, and OFFLINE when it does not.
//     Which of the two it is comes from the platform (an inferred device goes offline after its
//     inactivity timeout; an asserted one follows its transport's connect and disconnect), never
//     from this widget's idea of how long is too long;
//   - a device with NO state row has never sent anything: it is neither online nor offline, and is
//     counted apart ("no status yet"), never folded into the offline figure -- a device that was never
//     commissioned is not an outage;
//   - a member whose read is still pending, or failed, is counted apart again. While any is pending
//     there is no tally: a share over the members read so far would be an answer that looks final;
//   - "last seen" is the device's last activity (device-state's lastActivityTime), the time of its
//     newest event, not the time it was last marked connected.

import type { FleetMember, MemberPresence } from '@devicechain/dashboards';

export type PresenceKind = 'online' | 'offline' | 'unknown' | 'pending' | 'failed';

export function presenceKind(presence: MemberPresence | undefined): PresenceKind {
  if (!presence) return 'pending';
  if (presence.status === 'ready') return presence.active ? 'online' : 'offline';
  return presence.status;
}

// The device's last activity (epoch ms), or undefined when it has none or it does not parse.
export function lastActivityMs(presence: MemberPresence | undefined): number | undefined {
  if (presence?.status !== 'ready' || !presence.lastActivityTime) return undefined;
  const at = Date.parse(presence.lastActivityTime);
  return Number.isNaN(at) ? undefined : at;
}

export interface PresenceTally {
  online: number;
  offline: number;
  // No state row: the device has never sent anything.
  unknown: number;
  pending: number;
  failed: number;
  // The newest last-activity time among the members (epoch ms), when any has one.
  newest: number | undefined;
}

export function tallyPresence(members: readonly FleetMember[]): PresenceTally {
  const tally: PresenceTally = { online: 0, offline: 0, unknown: 0, pending: 0, failed: 0, newest: undefined };
  for (const m of members) {
    tally[presenceKind(m.presence)]++;
    const at = lastActivityMs(m.presence);
    if (at !== undefined && (tally.newest === undefined || at > tally.newest)) tally.newest = at;
  }
  return tally;
}
