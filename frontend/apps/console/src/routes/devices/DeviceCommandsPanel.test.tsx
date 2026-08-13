// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real catalogs are wired in by
// importing the i18n config for its side effect, so the assertions below are on the
// CONTROL AN OPERATOR SEES rather than on a prop or a translation key.
//
// Only one seam is faked: the GraphQL transport (`gql`). The panel's real terminality
// rule decides which rows offer Cancel.
//
// 🔴 WHY THIS FILE EXISTS. The panel used to keep its own copy of the terminal status
// set. When cancellation started writing CANCELLED instead of EXPIRED, that copy never
// learned the new value, so an already-cancelled command still rendered a Cancel button —
// and clicking it produced a server error toast. Nothing in the frontend asserted the
// SET of statuses, so it broke in total silence.
import '@/i18n/config';
import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

// Spread the real module so everything except the wire stays genuine.
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DeviceCommandsPanel } from './DeviceCommandsPanel';

afterEach(cleanup);
beforeEach(() => gqlMock.mockReset());

const CANCEL = 'Cancel';

// One history row per status, named after the status so a row is addressable by the
// command name — which is what lets each assertion be scoped to ITS row rather than to
// "somewhere on the page".
function commandRow(status: string) {
  return {
    id: `id-${status}`,
    token: `tok-${status}`,
    deviceToken: 'therm-1',
    name: `cmd-${status}`,
    payload: null,
    status,
    queuedTime: '2026-08-12T12:00:00Z',
    sentTime: null,
    respondedTime: null,
    expiresAt: null,
    responsePayload: null,
    error: null,
  };
}

// Answers dispatch on the service: the panel loads its history from command-delivery and
// the device's command vocabulary from device-management, independently.
function respondWith(statuses: string[]) {
  gqlMock.mockImplementation((service: string) => {
    if (service === 'command-delivery') {
      return Promise.resolve({
        commands: {
          results: statuses.map(commandRow),
          pagination: { pageStart: 0, pageEnd: statuses.length, totalRecords: statuses.length },
        },
      });
    }
    // Unconstrained: the profile declares no vocabulary, so the issue form is free text.
    // Irrelevant to the history table, but the panel waits on it before rendering.
    return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
  });
}

// The Cancel control inside the row for a given status, or null when none is offered.
function cancelControlFor(status: string): HTMLElement | null {
  const row = screen.getByText(`cmd-${status}`).closest('tr');
  if (!row) throw new Error(`no row rendered for ${status}`);
  return within(row).queryByRole('button', { name: CANCEL });
}

describe('DeviceCommandsPanel cancel control', () => {
  // 🔴🔴 THE ONE THAT MATTERS. CANCELLED is terminal: the server refuses a second
  // cancellation, so offering the button can only ever produce an error toast. The
  // non-terminal three are asserted in the SAME render as the counterweight — a panel
  // that had simply stopped offering Cancel altogether would satisfy the negative
  // assertion perfectly and fail here.
  it('offers Cancel only while a command can still be cancelled', async () => {
    respondWith(['QUEUED', 'HELD', 'SENT', 'SUCCESSFUL', 'FAILED', 'TIMEOUT', 'EXPIRED', 'CANCELLED']);

    render(<DeviceCommandsPanel deviceToken="therm-1" />);

    // The history has painted once the first row is on screen.
    expect(await screen.findByText('cmd-QUEUED')).toBeTruthy();

    for (const live of ['QUEUED', 'HELD', 'SENT']) {
      expect(cancelControlFor(live), `${live} is still in flight and must offer Cancel`).toBeTruthy();
    }
    for (const done of ['SUCCESSFUL', 'FAILED', 'TIMEOUT', 'EXPIRED', 'CANCELLED']) {
      expect(cancelControlFor(done), `${done} is terminal and must NOT offer Cancel`).toBeNull();
    }
    // Exactly three, so a stray button elsewhere in a terminal row can't hide behind the
    // per-row queries above.
    expect(screen.getAllByRole('button', { name: CANCEL })).toHaveLength(3);
  });

  // HELD is the new non-terminal state, and it is the one most likely to be mistaken for
  // a dead command: it can sit for days waiting for an absent device. It is displayed
  // like the other in-flight states, and it keeps its Cancel control — an operator whose
  // command is stuck behind an offline machine needs exactly that button.
  it('shows a held command as still in flight rather than as an outcome', async () => {
    respondWith(['HELD', 'EXPIRED']);

    render(<DeviceCommandsPanel deviceToken="therm-1" />);

    // The status is rendered as the raw uppercase value the service sends — there is no
    // translation catalog for statuses, and this test pins that the new ones are shown
    // exactly like the six that came before.
    expect(await screen.findByText('HELD')).toBeTruthy();
    expect(cancelControlFor('HELD')).toBeTruthy();
    expect(cancelControlFor('EXPIRED')).toBeNull();
  });
});
