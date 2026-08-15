// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 WHY THIS EXISTS. This mapping was a file-local switch inside DeviceCommandsPanel,
// and a second screen (a batch's per-device rows) needs the identical one. The way that
// goes wrong is not a crash: it is two tables painting the same status differently, so
// an operator reading a batch believes something different about a FAILED command than
// the device page told them. Promoting it to one module only helps if the mapping is
// pinned, so every status the service can persist is asserted here BY NAME.
//
// The enumeration is driven off COMMAND_STATUSES — the service's vocabulary as the
// frontend holds it — rather than a list retyped here, so a status added to that set
// with no answer in the mapping fails this file instead of silently falling through to
// the in-flight default.

import { describe, expect, it } from 'vitest';
import { COMMAND_STATUSES } from '@devicechain/dashboards';

import { commandStatusVariant, type CommandStatusVariant } from './command-status';

// The intended appearance of every persisted status, stated as data.
const EXPECTED: Record<string, CommandStatusVariant> = {
  // Still in flight, or waiting on the device. All four read the same on purpose.
  QUEUED: 'secondary',
  HELD: 'secondary',
  SENT: 'secondary',
  PARKED: 'secondary',
  // The device answered, and it worked.
  SUCCESSFUL: 'success',
  // A genuine fault an operator should chase.
  FAILED: 'destructive',
  TIMEOUT: 'destructive',
  // Terminal, but nothing broke — muted rather than red.
  EXPIRED: 'outline',
  CANCELLED: 'outline',
};

describe('commandStatusVariant', () => {
  it('answers for every status the service can persist', () => {
    // Nine today. Asserted as a count as well as by name so a status ADDED to the
    // shared vocabulary cannot slip past the table below by simply not being in it.
    expect(COMMAND_STATUSES).toHaveLength(9);
    for (const status of COMMAND_STATUSES) {
      expect(EXPECTED[status], `${status} has no expected variant`).toBeDefined();
      expect(commandStatusVariant(status), status).toBe(EXPECTED[status]);
    }
  });

  // 🔴 THE COUNTERWEIGHT. Every in-flight state maps to 'secondary', so a function that
  // simply RETURNED 'secondary' would satisfy four of the nine cases above and read as
  // mostly right. These two pin the distinctions that carry meaning: a failure is red,
  // a success is green, and a command that merely stopped is neither.
  it('distinguishes a failure from a command that merely stopped', () => {
    expect(commandStatusVariant('FAILED')).not.toBe(commandStatusVariant('CANCELLED'));
    expect(commandStatusVariant('TIMEOUT')).toBe('destructive');
    expect(commandStatusVariant('EXPIRED')).toBe('outline');
  });

  it('distinguishes success from everything else', () => {
    expect(commandStatusVariant('SUCCESSFUL')).toBe('success');
    for (const status of COMMAND_STATUSES) {
      if (status === 'SUCCESSFUL') continue;
      expect(commandStatusVariant(status), status).not.toBe('success');
    }
  });

  // Status crosses the wire as a plain string with no GraphQL enum behind it, so a value
  // this build has never heard of is always reachable. It must render, not throw, and it
  // must not claim an outcome — the in-flight styling is the honest answer.
  it('renders an unrecognized status as in flight rather than as an outcome', () => {
    expect(commandStatusVariant('SUPERSEDED')).toBe('secondary');
    expect(commandStatusVariant('')).toBe('secondary');
  });
});
