// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Whether a command is, right now, waiting for its device type's maintenance window.
//
// The command-delivery service stamps `dispatchAfter` on a command it held for a window and
// leaves the status QUEUED (or HELD / PARKED) while it waits. A `dispatchAfter` in the past
// only records how long the command waited, so it says nothing special; and a command whose
// status has moved on (SENT, RESPONDED, CANCELLED, …) is no longer waiting whatever the stamp
// says. The server decided all of that: this only reads it back.

/** The statuses in which a command can still be held back by a window. */
const WAITING_STATUSES: ReadonlySet<string> = new Set(['QUEUED', 'HELD', 'PARKED']);

export interface CommandWait {
  /** When the command may first be sent. */
  until: Date;
  /** The window the hold came from, when the service recorded it. */
  windowToken: string | null;
}

/**
 * The wait to show for a command, or null when it is not waiting.
 *
 * @param now injectable for tests; defaults to the current time
 */
export function commandWait(
  command: {
    status: string;
    dispatchAfter?: string | null;
    maintenanceWindowToken?: string | null;
  },
  now: Date = new Date(),
): CommandWait | null {
  if (!WAITING_STATUSES.has(command.status)) return null;
  if (!command.dispatchAfter) return null;
  const until = new Date(command.dispatchAfter);
  if (Number.isNaN(until.getTime())) return null;
  if (until.getTime() <= now.getTime()) return null;
  return { until, windowToken: command.maintenanceWindowToken ?? null };
}
