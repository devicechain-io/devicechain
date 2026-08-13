// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command delivery-status presentation for the command-button widget (command-delivery
// lifecycle: QUEUED → SENT → SUCCESSFUL | FAILED, with HELD for a withheld dispatch and
// TIMEOUT / EXPIRED / CANCELLED as the other terminals).
// Status color is SEMANTIC (good/working/waiting/bad), deliberately fixed rather than
// derived from the dashboard accent, so the same status reads the same on every theme —
// and it mirrors the console's DeviceCommandsPanel status variants (success /
// destructive / outline / in-flight) so an operator reads the same signal on both
// surfaces.

import { isTerminalCommandStatus } from '@devicechain/dashboards';

// 🔴 EVERY STATUS NEEDS AN ENTRY, INCLUDING THE ONES THAT LOOK NEUTRAL. UNKNOWN_COLOR
// below is the same slate as EXPIRED, so a status with no entry does not merely fall
// back — it becomes INDISTINGUISHABLE from "lapsed before it was sent". HELD (the
// command is still coming) rendered in EXPIRED's slate tells an operator the opposite
// of the truth, which is why HELD and CANCELLED are spelled out rather than left to the
// fallback.
const STATUS_COLORS: Record<string, string> = {
  SUCCESSFUL: '#16a34a', // green-600 — acknowledged by the device
  FAILED: '#dc2626', // red-600
  TIMEOUT: '#dc2626', // red-600 — never acknowledged in time
  EXPIRED: '#64748b', // slate-500 — lapsed before it was ever sent
  CANCELLED: '#7c3aed', // violet-600 — deliberately called off; a decision, not a failure
  QUEUED: '#0284c7', // sky-600 — in flight
  SENT: '#0284c7',
  HELD: '#d97706', // amber-600 — withheld: the device is absent, the command still stands
};

const UNKNOWN_COLOR = '#64748b';

// commandStatusColor maps a status to its badge color, falling back to muted slate for
// an unrecognized value (a future/hand-edited status).
export function commandStatusColor(status: string): string {
  return STATUS_COLORS[status] ?? UNKNOWN_COLOR;
}

// isTerminalStatus reports whether a command has reached a state it can't leave — the
// widget stops treating it as in-flight (and a host could hide a cancel control).
//
// It is an alias, not a second definition: the terminal set lives in @devicechain/dashboards
// so the console, this package and the preview cannot disagree about it. The name is kept
// because it is part of this package's published surface.
export function isTerminalStatus(status: string): boolean {
  return isTerminalCommandStatus(status);
}

// commandStatusLabel renders a status in Title Case ('SUCCESSFUL' → 'Successful').
export function commandStatusLabel(status: string): string {
  if (!status) return '';
  return status.charAt(0).toUpperCase() + status.slice(1).toLowerCase();
}
