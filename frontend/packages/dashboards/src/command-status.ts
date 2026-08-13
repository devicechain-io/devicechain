// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command-delivery lifecycle vocabulary, as the frontend sees it.
//
// 🔴 THIS IS THE ONE COPY. It lives in `dashboards` rather than `widgets` because
// `widgets` depends on `dashboards` (never the reverse), so this is the lowest layer
// both the widget package and the console can reach. It previously existed as three
// hand-maintained sets — the console's DeviceCommandsPanel, the widgets' command-status
// helper, and an ad-hoc expression in the synthetic preview — which is exactly how the
// console kept offering a Cancel button on an already-cancelled command: a status was
// added to the service and only some of the copies learned about it.
//
// The service declares no GraphQL enum for status, so it crosses the wire as a plain
// string and an UNRECOGNIZED value must stay survivable: callers treat an unknown status
// as non-terminal (still in flight) rather than throwing.

// Every lifecycle state the command-delivery service can persist. Ordered as a command
// travels: the non-terminal states first, then the terminal ones.
export const COMMAND_STATUSES = [
  // ── Non-terminal ───────────────────────────────────────────────────────
  // Accepted; awaiting its first dispatch decision. Genuinely transient.
  'QUEUED',
  // The platform is deliberately WITHHOLDING dispatch because the device is known
  // absent. This is where an offline fleet's backlog accumulates — it can sit for days,
  // and it is the honest answer to "why hasn't my command arrived?".
  'HELD',
  // Dispatched toward a device believed reachable; awaiting its response.
  'SENT',
  // ── Terminal ───────────────────────────────────────────────────────────
  // The device answered.
  'SUCCESSFUL',
  'FAILED',
  // Dispatched, never answered in time.
  'TIMEOUT',
  // The TTL elapsed before it ever went out.
  'EXPIRED',
  // An operator or tenant called it off. Distinct from EXPIRED because it is a
  // different ACTOR, not a different outcome. Cancellation used to write EXPIRED, so
  // BOTH values appear in real data — historical rows are not backfilled.
  'CANCELLED',
] as const;

export type CommandStatus = (typeof COMMAND_STATUSES)[number];

// The states a command can never leave: no further transition is permitted, so it can
// no longer be cancelled either.
export const TERMINAL_COMMAND_STATUSES: ReadonlySet<string> = new Set<string>([
  'SUCCESSFUL',
  'FAILED',
  'TIMEOUT',
  'EXPIRED',
  'CANCELLED',
]);

// isTerminalCommandStatus reports whether a command has reached a state it can't leave.
// An unrecognized status is reported as NON-terminal: the surfaces that consume this
// gate a cancel control on it, and refusing to offer cancel for a state we simply do
// not recognize would strand a live command with no way to call it off.
export function isTerminalCommandStatus(status: string): boolean {
  return TERMINAL_COMMAND_STATUSES.has(status);
}
