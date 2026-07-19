// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command delivery-status presentation for the command-button widget (command-delivery
// lifecycle: QUEUED → SENT → SUCCESSFUL | FAILED, plus TIMEOUT / EXPIRED).
// Status color is SEMANTIC (good/working/bad), deliberately fixed rather than derived
// from the dashboard accent, so the same status reads the same on every theme — and it
// mirrors the console's DeviceCommandsPanel status variants (success / destructive /
// outline / in-flight) so an operator reads the same signal on both surfaces.

// Commands past a terminal state can no longer transition (or be cancelled) — mirrors
// the console's TERMINAL set.
const TERMINAL = new Set(['SUCCESSFUL', 'TIMEOUT', 'EXPIRED', 'FAILED']);

const STATUS_COLORS: Record<string, string> = {
  SUCCESSFUL: '#16a34a', // green-600 — acknowledged by the device
  FAILED: '#dc2626', // red-600
  TIMEOUT: '#dc2626', // red-600 — never acknowledged in time
  EXPIRED: '#64748b', // slate-500 — cancelled / lapsed before it was sent
  QUEUED: '#0284c7', // sky-600 — in flight
  SENT: '#0284c7',
};

const UNKNOWN_COLOR = '#64748b';

// commandStatusColor maps a status to its badge color, falling back to muted slate for
// an unrecognized value (a future/hand-edited status).
export function commandStatusColor(status: string): string {
  return STATUS_COLORS[status] ?? UNKNOWN_COLOR;
}

// isTerminalStatus reports whether a command has reached a state it can't leave — the
// widget stops treating it as in-flight (and a host could hide a cancel control).
export function isTerminalStatus(status: string): boolean {
  return TERMINAL.has(status);
}

// commandStatusLabel renders a status in Title Case ('SUCCESSFUL' → 'Successful').
export function commandStatusLabel(status: string): string {
  if (!status) return '';
  return status.charAt(0).toUpperCase() + status.slice(1).toLowerCase();
}
