// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command lifecycle vocabulary is the thing three surfaces (console panel,
// command-button widget, preview) used to each keep their own copy of. Nothing asserted
// the SET, so when the service gained CANCELLED and HELD the copies just quietly
// disagreed — and the console went on offering a Cancel button for an already-cancelled
// command, which came back as a server error.
//
// These tests pin the set itself.

import { describe, expect, it } from 'vitest';

import {
  COMMAND_STATUSES,
  TERMINAL_COMMAND_STATUSES,
  isTerminalCommandStatus,
} from './command-status';

// 🔴 WRITTEN OUT BY HAND, ON PURPOSE. Deriving this from TERMINAL_COMMAND_STATUSES
// would make the test a restatement of the code: every status would "agree" no matter
// what the set said, including the empty set. The expectation has to come from the
// service's own definition of the lifecycle, which is what this literal is.
const EXPECTED_TERMINALITY: Record<string, boolean> = {
  // Non-terminal — still cancellable, still going somewhere.
  QUEUED: false,
  HELD: false, // withheld because the device is absent; it can sit for days and still send
  SENT: false,
  // Terminal — no transition out, so no cancel control either.
  SUCCESSFUL: true,
  FAILED: true,
  TIMEOUT: true,
  EXPIRED: true,
  CANCELLED: true, // the one that was missing; cancellation used to write EXPIRED
};

describe('command lifecycle vocabulary', () => {
  it('enumerates every status the service can persist, and no others', () => {
    expect([...COMMAND_STATUSES].sort()).toEqual(Object.keys(EXPECTED_TERMINALITY).sort());
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

  // The status crosses the wire as a plain string — the service declares no GraphQL
  // enum — so a value this build has never heard of is reachable. It must read as
  // non-terminal: the surfaces gate a cancel control on this, and calling an unknown
  // state terminal would strand a live command with no way to call it off.
  it('treats an unrecognized status as non-terminal', () => {
    expect(isTerminalCommandStatus('DELIVERED')).toBe(false);
    expect(isTerminalCommandStatus('')).toBe(false);
    expect(isTerminalCommandStatus('cancelled')).toBe(false); // case-sensitive, like the wire
  });
});
