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
// string and an UNRECOGNIZED value must stay survivable: nothing here throws on one. What
// an unknown status MEANS differs per question, though — see the two predicates at the
// bottom, which answer for it in opposite directions on purpose.
//
// 🔴 "IN FLIGHT" AND "CANCELLABLE" ARE NOT THE SAME SET, and reading one off the other is
// the bug this file is shaped to prevent. SENT is in flight (the device has not answered
// yet) but NOT cancellable: the command is already at the device, and letting the platform
// call it off would only make it discard the real answer that is on its way back.

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
  // Published, and it went NOWHERE: the device turned out not to be reachable, so the
  // transport had nothing to hand it to. The platform still holds the command and will
  // deliver it when the device next wakes. Distinct from HELD (where dispatch was never
  // attempted, because the device was already known absent) and from TIMEOUT (where the
  // command reached a device that then said nothing).
  'PARKED',
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

// The states a command can never leave: no further transition is permitted, so nothing
// more will happen to it. Terminal implies not cancellable; the converse does NOT hold.
export const TERMINAL_COMMAND_STATUSES: ReadonlySet<string> = new Set<string>([
  'SUCCESSFUL',
  'FAILED',
  'TIMEOUT',
  'EXPIRED',
  'CANCELLED',
]);

// The states from which the platform will still accept a cancellation — a POSITIVE list,
// mirroring the service's own gate. All three are states in which the command has not
// reached the device: QUEUED (not yet dispatched), HELD (dispatch withheld), PARKED
// (published into a void and waiting for the device to wake). Cancelling any of them
// takes back something nobody else is holding.
//
// SENT is deliberately absent. It is in flight, so !isTerminalCommandStatus('SENT') is
// true — which is exactly why a cancel control must not be gated on that expression.
export const CANCELLABLE_COMMAND_STATUSES: ReadonlySet<string> = new Set<string>([
  'QUEUED',
  'HELD',
  'PARKED',
]);

// 🔴 THE TWO PREDICATES ANSWER FOR AN UNRECOGNIZED STATUS IN OPPOSITE DIRECTIONS, on
// purpose. Status crosses the wire as a plain string with no GraphQL enum behind it, so a
// value this build has never heard of is always reachable — a newer service, a hand-edited
// row — and each question has its own safe side:
//
//   isTerminalCommandStatus     negative membership ⇒ unknown reads NON-terminal, i.e.
//                               STILL IN FLIGHT. A status the service adds is one it added
//                               because something is still happening to the command, and
//                               the cost of guessing wrong is only a row that lingers in an
//                               "outstanding" list instead of settling.
//
//   isCancellableCommandStatus  positive membership ⇒ unknown reads NOT CANCELLABLE. This
//                               file used to argue the other way — that refusing to offer
//                               cancel for a state we do not recognize would strand a live
//                               command — and that argument is now false. The service gates
//                               cancellation on the same positive list, and 🔴 OUTSIDE THAT
//                               LIST IT DOES NOT REFUSE: it SUCCEEDS and returns the command
//                               UNCHANGED. So a Cancel button offered on an unknown status is
//                               a no-op that answers the click with a SUCCESS TOAST for a
//                               command nobody cancelled — a worse outcome than an error,
//                               because the operator has no way to tell. The command is
//                               stranded either way; this way we do not also claim otherwise.
//                               A host that offers the button anyway must therefore check the
//                               status that CAME BACK, not the fact that the call resolved.
export function isTerminalCommandStatus(status: string): boolean {
  return TERMINAL_COMMAND_STATUSES.has(status);
}

// isCancellableCommandStatus reports whether a cancel request would be accepted. Gate every
// cancel control on THIS, never on !isTerminalCommandStatus — the two differ by SENT, and
// by every status added after this build shipped.
export function isCancellableCommandStatus(status: string): boolean {
  return CANCELLABLE_COMMAND_STATUSES.has(status);
}
