// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command lifecycle vocabulary is the thing three surfaces (console panel,
// command-button widget, preview) used to each keep their own copy of. Nothing asserted
// the SET, so when the service gained CANCELLED and HELD the copies just quietly
// disagreed — and the console went on offering a Cancel button for an already-cancelled
// command, whose click the service answered with SUCCESS and the unchanged row.
//
// These tests pin the set itself.

import { describe, expect, it } from 'vitest';

import {
  CANCELLABLE_COMMAND_STATUSES,
  COMMAND_STATUSES,
  TERMINAL_COMMAND_STATUSES,
  isCancellableCommandStatus,
  isTerminalCommandStatus,
} from './command-status';

// 🔴 WRITTEN OUT BY HAND, ON PURPOSE. Deriving this from TERMINAL_COMMAND_STATUSES
// would make the test a restatement of the code: every status would "agree" no matter
// what the set said, including the empty set. The expectation has to come from the
// service's own definition of the lifecycle, which is what this literal is.
const EXPECTED_TERMINALITY: Record<string, boolean> = {
  // Non-terminal — still going somewhere. NOT the same claim as "still cancellable":
  // SENT is here and is not cancellable. See EXPECTED_CANCELLABILITY below.
  QUEUED: false,
  HELD: false, // withheld because the device is absent; it can sit for days and still send
  SENT: false,
  PARKED: false, // published into a void; the platform still holds it for the device's return
  // Terminal — no transition out, so no cancel control either.
  SUCCESSFUL: true,
  FAILED: true,
  TIMEOUT: true,
  EXPIRED: true,
  CANCELLED: true, // the one that was missing; cancellation used to write EXPIRED
};

// 🔴 THE SECOND TABLE, AND IT IS NOT THE NEGATION OF THE FIRST. Written out by hand for
// the same reason: derived from either the code or the terminality table it would agree
// with whatever it was derived from. The whole point is the row where the two tables
// disagree — SENT, in flight yet uncancellable, because the command is already AT the
// device and cancelling it would only make the platform throw away the answer coming back.
const EXPECTED_CANCELLABILITY: Record<string, boolean> = {
  QUEUED: true, // never dispatched
  HELD: true, // dispatch deliberately withheld
  PARKED: true, // dispatched to nobody; still ours to take back
  SENT: false, // 🔴 in flight, NOT cancellable — the disagreement this table exists for
  SUCCESSFUL: false,
  FAILED: false,
  TIMEOUT: false,
  EXPIRED: false,
  CANCELLED: false,
};

describe('command lifecycle vocabulary', () => {
  it('enumerates every status the service can persist, and no others', () => {
    expect([...COMMAND_STATUSES].sort()).toEqual(Object.keys(EXPECTED_TERMINALITY).sort());
    // Both hand-written tables have to cover the whole vocabulary, or the next status
    // added would be classified by one of them and silently skipped by the other.
    expect(Object.keys(EXPECTED_CANCELLABILITY).sort()).toEqual(
      Object.keys(EXPECTED_TERMINALITY).sort(),
    );
  });

  it('classifies every status, including the two newest', () => {
    for (const [status, terminal] of Object.entries(EXPECTED_TERMINALITY)) {
      expect(isTerminalCommandStatus(status), `${status} terminality`).toBe(terminal);
    }
    // …and the exported set says the same thing the predicate does.
    expect([...TERMINAL_COMMAND_STATUSES].sort()).toEqual(
      Object.keys(EXPECTED_TERMINALITY)
        .filter((s) => EXPECTED_TERMINALITY[s])
        .sort(),
    );
  });

  it('classifies cancellability separately from terminality', () => {
    for (const [status, cancellable] of Object.entries(EXPECTED_CANCELLABILITY)) {
      expect(isCancellableCommandStatus(status), `${status} cancellability`).toBe(cancellable);
    }
    // …and the exported set says the same thing the predicate does.
    expect([...CANCELLABLE_COMMAND_STATUSES].sort()).toEqual(
      Object.keys(EXPECTED_CANCELLABILITY)
        .filter((s) => EXPECTED_CANCELLABILITY[s])
        .sort(),
    );
  });

  // 🔴🔴 THE ASSERTION THAT STOPS ONE PREDICATE BEING REIMPLEMENTED AS THE OTHER. A cancel
  // control gated on !isTerminalCommandStatus passes every other test in this file and is
  // still wrong on exactly this row — a live Cancel button on a command that is already at
  // the device, which the service leaves exactly where it is while answering OK.
  it('is in flight but NOT cancellable once SENT', () => {
    expect(isTerminalCommandStatus('SENT')).toBe(false);
    expect(isCancellableCommandStatus('SENT')).toBe(false);
    // PARKED is the counterweight: also post-publish, also non-terminal, but the device
    // never took it, so it IS still cancellable. Without this the pair above would be
    // satisfied by a predicate that simply refuses everything after dispatch.
    expect(isTerminalCommandStatus('PARKED')).toBe(false);
    expect(isCancellableCommandStatus('PARKED')).toBe(true);
  });

  // The status crosses the wire as a plain string — the service declares no GraphQL
  // enum — so a value this build has never heard of is reachable, and the two predicates
  // answer for it in OPPOSITE directions. Non-terminal, because a status the service just
  // added is one where something is still happening. Not cancellable, because the service
  // gates cancellation on its own positive list and, outside it, returns the command
  // unchanged with no error — making the button a no-op that reports SUCCESS to whoever
  // pressed it, for a command it did not touch.
  it('treats an unrecognized status as non-terminal but not cancellable', () => {
    for (const unknown of ['DELIVERED', '', 'cancelled', 'parked']) {
      // 'cancelled' / 'parked': case-sensitive, like the wire.
      expect(isTerminalCommandStatus(unknown), `${unknown} terminality`).toBe(false);
      expect(isCancellableCommandStatus(unknown), `${unknown} cancellability`).toBe(false);
    }
  });
});
