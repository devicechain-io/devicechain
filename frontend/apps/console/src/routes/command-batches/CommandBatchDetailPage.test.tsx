// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts), with the real i18n catalogs, the
// real API module and the real query documents. Only the GraphQL transport is faked, so
// the variables asserted below are the variables that would go on the wire.
//
// 🔴🔴 WHY THIS FILE EXISTS. A batch carries its refusals TWICE, and the two shapes are
// not the same set: `refusalCounts` is exact per code, while `refusals` is a SAMPLE
// capped at 100 devices per code. A panel that counts the sample tells an operator that
// 100 devices were refused when 4,312 were — and the 4,212 it did not mention are
// exactly the devices that never received the command. Nothing about the screen would
// look wrong. So the capped case is asserted here, in the numbers an operator reads.

import '@/i18n/config';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The header's CopyToken chip reports a clipboard failure through the toast; stubbing it
// keeps the page free of a provider it does not otherwise need.
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import CommandBatchDetailPage from './CommandBatchDetailPage';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

interface Refusal {
  deviceToken: string;
  code: string;
  reason: string;
}

function batch(over: Record<string, unknown> = {}) {
  return {
    id: 'id-1',
    token: 'batch-1',
    createdAt: '2026-08-12T12:00:00Z',
    name: 'reboot',
    payload: '{"delaySeconds":30}',
    targetKind: 'GROUP',
    groupToken: 'north-hvac',
    groupVersion: 3,
    allowPartial: true,
    resolved: 10,
    accepted: 8,
    refusals: [] as Refusal[],
    refusalCounts: [] as { code: string; count: number }[],
    cancelledAt: null,
    cancelledCount: null,
    ...over,
  };
}

function commandRow(deviceToken: string, status = 'QUEUED') {
  return {
    id: `id-${deviceToken}`,
    token: `tok-${deviceToken}`,
    deviceToken,
    name: 'reboot',
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

// Answers dispatch on the DOCUMENT: the page reads the batch record and its command rows
// with two different operations against the same service.
function respondWith(
  found: ReturnType<typeof batch> | null,
  commands: ReturnType<typeof commandRow>[] = [],
) {
  gqlMock.mockImplementation((_service: string, document: unknown) => {
    if (String(document).includes('query CommandBatchesByToken')) {
      return Promise.resolve({ commandBatchesByToken: found ? [found] : [] });
    }
    return Promise.resolve({
      commands: {
        results: commands,
        pagination: { pageStart: 1, pageEnd: commands.length, totalRecords: commands.length },
      },
    });
  });
}

function renderPage(token = 'batch-1') {
  render(
    <MemoryRouter initialEntries={[`/command-batches/${token}`]}>
      <Routes>
        <Route path="/command-batches/:token" element={<CommandBatchDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

/** The variables of the most recent `commands` query. */
function lastCommandsCriteria(): Record<string, unknown> {
  // Indexed rather than .at(-1): the console's tsconfig lib predates ES2022, so `.at`
  // type-checks nowhere here even though vitest transpiles it happily.
  const commandsCalls = gqlMock.mock.calls.filter((c) => String(c[1]).includes('query Commands'));
  const call = commandsCalls[commandsCalls.length - 1];
  if (!call) throw new Error('the batch’s command rows were never queried');
  return (call[2] as { criteria: Record<string, unknown> }).criteria;
}

describe('CommandBatchDetailPage refusals', () => {
  // 🔴🔴 THE ONE THAT MATTERS.
  it('reports the exact refusal count, not the size of the capped sample', async () => {
    const sample = Array.from({ length: 100 }, (_, i) => ({
      deviceToken: `dev-${i}`,
      code: 'DEVICE_NOT_FOUND',
      reason: 'device is not registered',
    }));
    respondWith(
      batch({
        resolved: 4400,
        accepted: 88,
        refusals: sample,
        refusalCounts: [{ code: 'DEVICE_NOT_FOUND', count: 4312 }],
      }),
    );

    renderPage();

    // The EXACT total leads, and it is the count — not the 100 names below it.
    expect(await screen.findByText('4,312 devices refused')).toBeTruthy();
    // …and the sample is labelled as a sample, naming both numbers so the gap is visible.
    expect(
      screen.getByText(
        'Showing 100 of 4,312 refused devices — the list of names is capped, the total is exact.',
      ),
    ).toBeTruthy();
    // 🔴 The defect stated directly: the sample size must never be presented as the
    // number of refused devices.
    expect(screen.queryByText('100 devices refused')).toBeNull();
  });

  // The counterweight. A panel that ALWAYS said "showing N of M" would pass the test
  // above and lie in the far more common small-batch case, where the sample is complete.
  it('does not claim a cap when the sample names every refused device', async () => {
    respondWith(
      batch({
        resolved: 10,
        accepted: 8,
        refusals: [
          { deviceToken: 'dev-a', code: 'COMMAND_NOT_IN_VOCABULARY', reason: 'not published' },
          { deviceToken: 'dev-b', code: 'COMMAND_NOT_IN_VOCABULARY', reason: 'not published' },
        ],
        refusalCounts: [{ code: 'COMMAND_NOT_IN_VOCABULARY', count: 2 }],
      }),
    );

    renderPage();

    expect(await screen.findByText('2 devices refused')).toBeTruthy();
    expect(screen.getByText('Showing all 2 refused devices.')).toBeTruthy();
    expect(screen.queryByText(/the list of names is capped/)).toBeNull();
    // The code and the client-safe reason are the backend's own strings, English by
    // policy, and are rendered verbatim rather than translated or prettified.
    expect(screen.getAllByText('COMMAND_NOT_IN_VOCABULARY').length).toBeGreaterThan(0);
    expect(screen.getAllByText('not published').length).toBe(2);
  });

  it('says so plainly when nobody was refused', async () => {
    respondWith(batch({ resolved: 8, accepted: 8 }));

    renderPage();

    expect(
      await screen.findByText('Every device the target resolved to was accepted.'),
    ).toBeTruthy();
  });
});

describe('CommandBatchDetailPage summary', () => {
  // The three numbers are otherwise just three numbers. Stating the identity is what
  // makes the record self-auditing on screen.
  it('states the arithmetic that ties resolved to accepted and refused', async () => {
    respondWith(
      batch({
        resolved: 4400,
        accepted: 88,
        refusalCounts: [
          { code: 'DEVICE_NOT_FOUND', count: 4312 },
          { code: 'HELD_CEILING_EXCEEDED', count: 0 },
        ],
      }),
    );

    renderPage();

    expect(
      await screen.findByText('resolved = accepted + refused — 4400 = 88 + 4312'),
    ).toBeTruthy();
    // The stored-vs-live distinction is on screen too: these are facts about the moment
    // the batch fired, and the present tense lives in the Commands panel.
    expect(screen.getByText(/stored facts about the moment the batch fired/)).toBeTruthy();
  });

  it('carries the identity, target and partial-fan-out decision in the header', async () => {
    respondWith(batch({ allowPartial: false }));

    renderPage();

    expect(await screen.findByText('reboot')).toBeTruthy();
    expect(screen.getByText('north-hvac @ v3')).toBeTruthy();
    expect(screen.getByText('Group')).toBeTruthy();
    // allowPartial is stated either way — its absence must not read as "unset", because
    // it decided whether a physical actuation could reach part of a fleet.
    expect(screen.getByText('All or nothing')).toBeTruthy();
    // The token is the id everything references, so it rides the title line as a chip.
    expect(screen.getByRole('button', { name: 'Copy batch-1 to clipboard' })).toBeTruthy();
  });

  it('shows the payload read-only, and says when there is none', async () => {
    respondWith(batch());
    renderPage();

    const payload = await screen.findByLabelText('Payload');
    expect((payload as HTMLTextAreaElement).value).toBe('{"delaySeconds":30}');
    expect((payload as HTMLTextAreaElement).readOnly).toBe(true);

    cleanup();
    respondWith(batch({ payload: null }));
    renderPage();
    expect(await screen.findByText('No payload.')).toBeTruthy();
  });

  it('reports an unknown batch rather than an empty page', async () => {
    respondWith(null);

    renderPage('nope');

    expect(await screen.findByText('No command batch with token “nope”.')).toBeTruthy();
  });
});

describe('CommandBatchDetailPage commands', () => {
  // 🔴🔴 THE OTHER ONE THAT MATTERS. Without the batchToken criterion this panel lists
  // the TENANT'S commands — every command to every device — under the heading of this
  // batch. It would look entirely plausible: real rows, real statuses, wrong batch.
  it('asks only for the commands this batch created', async () => {
    // The token comes from the LOADED RECORD, not the URL, so the fixture names it.
    respondWith(batch({ token: 'batch-77' }), [commandRow('dev-a')]);

    renderPage('batch-77');

    await waitFor(() => expect(lastCommandsCriteria().batchToken).toBe('batch-77'));
    expect(lastCommandsCriteria().deviceToken).toBeUndefined();
    expect(lastCommandsCriteria().pageNumber).toBe(1);
  });

  // 🔴 ADMITTED ORDER, NOT SORTED. The rows arrive in the order the caller named the
  // devices in — which is the order a partially-admitted batch admitted them in — so
  // page 1 is the head of the batch. Any client-side sort destroys that.
  it('renders the rows in the order the service returned them', async () => {
    respondWith(batch(), [
      commandRow('zeta', 'SUCCESSFUL'),
      commandRow('alpha', 'FAILED'),
      commandRow('mike', 'QUEUED'),
    ]);

    renderPage();

    await screen.findByText('zeta');
    const devices = screen
      .getAllByRole('row')
      // Skip the header row, which has no device cell.
      .filter((r) => within(r).queryByText(/zeta|alpha|mike/))
      .map((r) => r.textContent ?? '');
    expect(devices[0]).toContain('zeta');
    expect(devices[1]).toContain('alpha');
    expect(devices[2]).toContain('mike');
  });

  it('shows each command’s live status', async () => {
    respondWith(batch(), [commandRow('dev-a', 'HELD'), commandRow('dev-b', 'SUCCESSFUL')]);

    renderPage();

    expect(await screen.findByText('HELD')).toBeTruthy();
    expect(screen.getByText('SUCCESSFUL')).toBeTruthy();
  });

  it('says so when the batch has no command rows to show', async () => {
    respondWith(batch(), []);

    renderPage();

    expect(await screen.findByText('No commands match.')).toBeTruthy();
  });
});
