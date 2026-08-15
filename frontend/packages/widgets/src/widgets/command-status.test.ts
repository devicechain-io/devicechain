// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Status PRESENTATION for the command-button widget. The colour map is the surface with
// the nastiest failure mode in this package: the unknown-status fallback is byte-identical
// to EXPIRED's slate, so a status nobody remembered to add does not look wrong — it looks
// EXPIRED. A held command (still coming) rendered as a lapsed one (dead) is the opposite
// of the truth, and no test would have noticed.

import { describe, expect, it } from 'vitest';
import { COMMAND_STATUSES, isTerminalCommandStatus } from '@devicechain/dashboards';

import { commandStatusColor, commandStatusLabel, isTerminalStatus } from './command-status';

// The fallback in command-status.ts. Duplicated here deliberately: the test needs to know
// the colour a FORGOTTEN status would render as, which is exactly what must not be
// reachable from a status the platform actually emits.
const UNKNOWN_FALLBACK = '#64748b';

describe('commandStatusColor', () => {
  // 🔴 THE ASSERTION THAT CATCHES THE SILENT COLLAPSE. Four distinct situations —
  // withheld, published-to-nobody, lapsed, called off — would collapse onto the same
  // slate if HELD, PARKED and CANCELLED were left to the fallback. PARKED is the newest
  // and the most damaging to lose: "still coming when the device wakes" wearing EXPIRED's
  // colour reads as "it died before it ever went out".
  it('gives HELD, PARKED, EXPIRED and CANCELLED four distinguishable colours', () => {
    const held = commandStatusColor('HELD');
    const parked = commandStatusColor('PARKED');
    const expired = commandStatusColor('EXPIRED');
    const cancelled = commandStatusColor('CANCELLED');

    expect(new Set([held, parked, expired, cancelled]).size).toBe(4);
  });

  // Spelled out, because the loop below only proves PARKED is not the FALLBACK — and the
  // fallback is slate, so a PARKED entry accidentally written as EXPIRED's own slate
  // '#64748b' would satisfy it while rendering the exact confusion this file exists to
  // prevent. Naming the colour also pins that PARKED sits in the amber "waiting on an
  // absent device" family with HELD rather than in the sky "moving" family with SENT.
  it('renders PARKED in the amber waiting family, never in a lapsed slate', () => {
    expect(commandStatusColor('PARKED')).toBe('#f59e0b');
    expect(commandStatusColor('PARKED')).not.toBe(commandStatusColor('EXPIRED'));
    expect(commandStatusColor('PARKED')).not.toBe(commandStatusColor('SENT'));
  });

  // The general form of the same rule, so the NEXT status added to the service can't
  // slip through. EXPIRED is the one legitimate occupant of the fallback colour (slate
  // IS its colour); any other status wearing it means no entry was written for it.
  it('gives every known status an explicit colour rather than the unknown fallback', () => {
    for (const status of COMMAND_STATUSES) {
      if (status === 'EXPIRED') continue;
      expect(commandStatusColor(status), `${status} has no colour of its own`).not.toBe(
        UNKNOWN_FALLBACK,
      );
    }
  });

  // command-button reuses commandStatusColor('FAILED') as its generic error red (required
  // markers, validation text, the dispatch-failure line). Restructuring the map must not
  // quietly change what "error" looks like across the widget.
  it('keeps FAILED on the error red the widget reuses for its own error text', () => {
    expect(commandStatusColor('FAILED')).toBe('#dc2626');
  });

  it('falls back to muted slate for a status this build has never heard of', () => {
    expect(commandStatusColor('DELIVERED')).toBe(UNKNOWN_FALLBACK);
  });
});

describe('isTerminalStatus', () => {
  // This package's export is an ALIAS for the one definition in @devicechain/dashboards.
  // Asserting agreement across every status is what stops it drifting back into a second
  // copy — the drift that left the console offering Cancel on a CANCELLED command.
  it('agrees with the canonical terminal set for every status', () => {
    for (const status of COMMAND_STATUSES) {
      expect(isTerminalStatus(status), status).toBe(isTerminalCommandStatus(status));
    }
    // Spelled out for the ones the widgets package never used to know about, so this test
    // reads as a claim about them rather than a loop that could pass vacuously.
    expect(isTerminalStatus('CANCELLED')).toBe(true);
    expect(isTerminalStatus('HELD')).toBe(false);
    // PARKED is non-terminal: the platform still holds it and will deliver it when the
    // device wakes. The widget must keep showing it as an outstanding command.
    expect(isTerminalStatus('PARKED')).toBe(false);
  });
});

describe('commandStatusLabel', () => {
  it('renders the new statuses in the same Title Case as the rest', () => {
    expect(commandStatusLabel('HELD')).toBe('Held');
    expect(commandStatusLabel('PARKED')).toBe('Parked');
    expect(commandStatusLabel('CANCELLED')).toBe('Cancelled');
  });
});
