// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the REAL i18n catalogs, the real confirm dialog, the real API
// module and the real query documents. Only the GraphQL transport and the auth claims are
// faked, so the copy asserted below is the copy an operator reads and the variables
// asserted are the variables that would go on the wire.
//
// 🔴🔴 WHY THIS FILE EXISTS. `cancelCommandBatch` returns four numbers that DELIBERATELY
// NEED NOT SUM, and every way of getting this screen wrong is a way of making them read
// better than they are:
//
//   • `alreadySent` counts commands that are AT their devices. THOSE DEVICES WILL STILL
//     ACT. A screen reporting "1,000 cancelled" while 300 of them are about to actuate
//     hardware is the worst thing this feature could do, and nothing about it would look
//     wrong — the number is even true, for a definition of "cancelled" nobody holds.
//
//   • `matched` above the sum means commands went back into the delivery queue and this
//     cancel MISSED them. Nothing retries on the operator's behalf, so a screen that
//     swallows the gap leaves live commands running with no sign of it.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, authorities } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  authorities: { value: ['command:read', 'command:write'] as string[] },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  // hasAuthority is deliberately NOT stubbed: the gate under test is the real one.
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));

// The header's CopyToken chip reports a clipboard failure through the toast.
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import CommandBatchDetailPage from './CommandBatchDetailPage';

interface CancelCounts {
  cancelled: number;
  alreadySent: number;
  alreadyFinished: number;
  matched: number;
}

// Mutable per-test wiring, so a cancel can change what the page re-reads afterwards.
const world = {
  batch: {} as Record<string, unknown>,
  heldCount: 0 as number | null,
  cancelResult: { cancelled: 0, alreadySent: 0, alreadyFinished: 0, matched: 0 } as CancelCounts,
  cancelThrows: null as Error | null,
  // Applied to the batch record the SECOND and later reads return — a real cancel stamps
  // cancelledAt, and the page re-reads.
  afterCancel: null as Record<string, unknown> | null,
  // Armed at the same moment, so a test can make the still-held count's RE-read fail while
  // its first read succeeded. That is the only way to reach useQuery's keep-the-prior-data
  // path, which is where a stale count masquerades as a fresh one.
  countThrowsAfterCancel: null as Error | null,
  countThrows: null as Error | null,
  // Holds every still-held count read open until a test releases them, so the confirmation
  // can be opened BEFORE its count lands — the ordering under which a handed-in count would
  // freeze "could not be read" into the dialog for as long as it stayed open.
  holdCount: false,
  countResolvers: [] as Array<() => void>,
  cancelCalls: [] as unknown[][],
};

function batch(over: Record<string, unknown> = {}) {
  return {
    id: 'id-1',
    token: 'batch-1',
    createdAt: '2026-08-12T12:00:00Z',
    name: 'reboot',
    payload: null,
    targetKind: 'GROUP',
    groupToken: 'north-hvac',
    groupVersion: 3,
    allowPartial: true,
    resolved: 10,
    accepted: 8,
    refusals: [] as unknown[],
    refusalCounts: [] as unknown[],
    cancelledAt: null,
    cancelledCount: null,
    ...over,
  };
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authorities.value = ['command:read', 'command:write'];
  world.batch = batch();
  world.heldCount = 4;
  world.cancelResult = { cancelled: 0, alreadySent: 0, alreadyFinished: 0, matched: 0 };
  world.cancelThrows = null;
  world.afterCancel = null;
  world.countThrowsAfterCancel = null;
  world.countThrows = null;
  world.holdCount = false;
  world.countResolvers = [];
  world.cancelCalls = [];

  gqlMock.mockImplementation((_service: string, document: unknown, variables: unknown) => {
    const doc = String(document);
    if (doc.includes('mutation CancelCommandBatch')) {
      world.cancelCalls.push([variables]);
      if (world.cancelThrows) return Promise.reject(world.cancelThrows);
      if (world.afterCancel) world.batch = { ...world.batch, ...world.afterCancel };
      if (world.countThrowsAfterCancel) world.countThrows = world.countThrowsAfterCancel;
      return Promise.resolve({ cancelCommandBatch: world.cancelResult });
    }
    if (doc.includes('query CommandBatchesByToken')) {
      return Promise.resolve({ commandBatchesByToken: [world.batch] });
    }
    // Both the still-held COUNT and the rows table use `query Commands`; the count is the
    // one that narrows by `statuses`.
    const criteria = (variables as { criteria: Record<string, unknown> }).criteria;
    if (criteria.statuses) {
      if (world.countThrows) return Promise.reject(world.countThrows);
      const answer = {
        commands: {
          results: [],
          pagination: { pageStart: 1, pageEnd: 1, totalRecords: world.heldCount },
        },
      };
      if (world.holdCount) {
        return new Promise((resolve) => {
          world.countResolvers.push(() => resolve(answer));
        });
      }
      return Promise.resolve(answer);
    }
    return Promise.resolve({
      commands: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } },
    });
  });
});

function renderPage() {
  render(
    <ConfirmProvider>
      <MemoryRouter initialEntries={['/command-batches/batch-1']}>
        <Routes>
          <Route path="/command-batches/:token" element={<CommandBatchDetailPage />} />
        </Routes>
      </MemoryRouter>
    </ConfirmProvider>,
  );
}

/** Press Cancel batch and confirm the dialog. */
async function cancelAndConfirm(buttonName = 'Cancel batch') {
  fireEvent.click(await screen.findByRole('button', { name: buttonName }));
  const dialog = await screen.findByRole('dialog');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel batch' }));
}

describe('the cancel action’s gates', () => {
  // 🔴 The authority gate is the real hasAuthority against the real claim shape.
  it('offers no cancel action without command:write', async () => {
    authorities.value = ['command:read'];

    renderPage();

    // The page itself has loaded — so the absence below is the gate, not an empty render.
    expect(await screen.findByText('Summary')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Cancel batch' })).toBeNull();
  });

  it('offers the cancel action with command:write', async () => {
    renderPage();

    expect(await screen.findByRole('button', { name: 'Cancel batch' })).toBeTruthy();
  });

  // A batch keeps the FIRST cancellation's stamp forever, so on one already called off and
  // holding nothing more, re-offering the action invites a write whose only visible effect
  // is a second report of an event that already happened.
  it('offers no cancel action on a called-off batch the platform is holding nothing of', async () => {
    world.batch = batch({ cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: 6 });
    world.heldCount = 0;

    renderPage();

    expect(await screen.findByText('Summary')).toBeTruthy();
    // S1's own rendering of the cancellation is still there — this is a cancelled batch,
    // not a broken page.
    expect(screen.getByText(/Called off on/)).toBeTruthy();
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /^Cancel/ })).toBeNull(),
    );
  });

  // 🔴🔴 THE GAP THIS GATE EXISTS TO CLOSE, and it is not a hypothetical: command-delivery
  // documents the interleaving that produces it. A release whose subquery saw no committed
  // stamp requeues a command whose batch is called off an instant later, and nothing
  // downstream re-checks — so the command is LIVE inside a CANCELLED batch. It becomes so
  // after the cancellation report has been rendered, so no reading of that report can find
  // it, and gating on the stamp alone would leave the operator looking at a running fleet
  // write with no control to stop it.
  it('offers the action again on a called-off batch the platform is still holding commands of', async () => {
    world.batch = batch({ cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: 6 });
    world.heldCount = 2;

    renderPage();

    // Worded for what pressing it does now — this is not the first cancellation.
    expect(await screen.findByRole('button', { name: 'Cancel again' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Cancel batch' })).toBeNull();
    // And it asks a question about what is LEFT, not about calling off a batch that is
    // already called off.
    fireEvent.click(screen.getByRole('button', { name: 'Cancel again' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Stop what is left of “reboot”?')).toBeTruthy();
    expect(
      await within(dialog).findByText(
        'The platform is still holding 2 of this batch’s commands and will stop them.',
      ),
    ).toBeTruthy();
  });

  // 🔴 AN UNKNOWN COUNT IS NOT ZERO. The count is a nullable Int and the read can simply
  // fail; neither says the batch is settled, and collapsing them into "nothing to stop"
  // withdraws a brake on the strength of an answer nobody gave.
  it('keeps offering the action on a called-off batch whose still-held count is unknown', async () => {
    world.batch = batch({ cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: 6 });
    world.heldCount = null;

    renderPage();

    expect(await screen.findByRole('button', { name: 'Cancel again' })).toBeTruthy();
  });

  // 🔴 A BATCH NOBODY HAS CALLED OFF KEEPS THE ACTION EVEN AT ZERO, and the reason is not
  // symmetry. The stamp is what makes command-delivery retire a failed delivery instead of
  // putting it back in the queue, so pressing cancel on a batch whose commands are all at
  // their devices still decides what happens to them if they fail. Withdrawing it here on
  // "nothing held" would take away a decision, not a no-op.
  it('offers the action on a live batch even when the platform is holding nothing', async () => {
    world.heldCount = 0;

    renderPage();

    expect(await screen.findByRole('button', { name: 'Cancel batch' })).toBeTruthy();
  });

  // 🔴 `cancelledCount` IS NULLABLE EVEN WHEN `cancelledAt` IS SET, so the wording must key
  // on the STAMP. Keying on the count would call a cancelled batch's action "Cancel batch"
  // purely because its count was not recorded.
  it('still treats the batch as called off when no count was recorded', async () => {
    world.batch = batch({ cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: null });
    world.heldCount = 2;

    renderPage();

    // No count in the sentence, because none was recorded — S1's nullable-count arm.
    expect(await screen.findByText(/^Called off on .*\.$/)).toBeTruthy();
    expect(screen.queryByText(/which stopped/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Cancel again' })).toBeTruthy();
  });

  // 🔴🔴 A FAILED RE-READ IS NOT A COUNT OF ZERO, and `useQuery` is what turns that from a
  // nicety into a live hazard: it deliberately KEEPS the previous data when a refetch fails,
  // so the value this gate reads after an unanswered read is the value from before it. On
  // this page the value from before is very often 0 — so reading `data` alone would let one
  // failed read withdraw the brake and keep it withdrawn.
  it('does not read a stale zero as a fresh one when the count re-read fails', async () => {
    world.heldCount = 0;
    world.afterCancel = { cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: 0 };
    world.countThrowsAfterCancel = new Error('command-delivery unreachable');

    renderPage();
    await cancelAndConfirm();

    // The stamp landed, so a stamp-only gate would hide the action from here on…
    expect(await screen.findByText(/Called off on/)).toBeTruthy();
    // …and the only count the gate can still see is the stale 0 from before the cancel.
    // The action stays on offer, because nobody answered the question after it.
    expect(await screen.findByRole('button', { name: 'Cancel again' })).toBeTruthy();
  });

  // The control for the test above: same batch, same zero, but the re-read LANDS. Without
  // this, "the button is showing" proves nothing — it would be showing either way.
  it('withdraws the action when the count re-read lands and says nothing is left', async () => {
    world.heldCount = 0;
    world.afterCancel = { cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: 0 };

    renderPage();
    await cancelAndConfirm();

    expect(await screen.findByText(/Called off on/)).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole('button', { name: /^Cancel/ })).toBeNull());
  });
});

describe('the confirmation', () => {
  it('names the batch and how much the platform is still holding', async () => {
    world.heldCount = 4;

    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel batch' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Call off “reboot”?')).toBeTruthy();
    // Awaited, because the dialog reads this count ITSELF when it opens rather than being
    // handed the page's — `confirm()` stores its description in state, so a handed-in value
    // is frozen at the instant of the click and cannot be corrected afterwards.
    expect(
      await within(dialog).findByText(
        'The platform is still holding 4 of this batch’s commands and will stop them.',
      ),
    ).toBeTruthy();
    // 🔴 Stated BEFORE the operator commits, not only in the report afterwards.
    expect(
      within(dialog).getByText(
        'Commands already handed to their devices cannot be recalled — those devices will still act on them.',
      ),
    ).toBeTruthy();
    // "Cancel" would name the thing being confirmed, so the dismiss button must not say it.
    expect(within(dialog).getByRole('button', { name: 'Leave it running' })).toBeTruthy();
    expect(within(dialog).queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  // A null total is three things at once — still loading, read failed, or a null
  // `totalRecords` — and all three mean nobody said how much is left to stop. Printing a
  // confident 0 would read as "there is nothing to stop".
  it('says the still-held count is unknown rather than printing zero', async () => {
    world.heldCount = null;

    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel batch' }));

    const dialog = await screen.findByRole('dialog');
    expect(
      within(dialog).getByText(
        'How many of this batch’s commands the platform still holds could not be read, so how much this stops is not known in advance.',
      ),
    ).toBeTruthy();
    expect(within(dialog).queryByText(/still holding 0 of/)).toBeNull();
  });

  // 🔴🔴 THE DIALOG READS THE COUNT ITSELF, and this is what that buys. `confirm()` stores
  // its description in the provider's state, so a count handed in at the click is a SNAPSHOT
  // — open the dialog while the read is in flight and it would say "could not be read" for
  // as long as it stayed open, with the true answer sitting one component away. The same
  // snapshot is why a page left open for ten minutes would state a ten-minute-old number in
  // the present tense.
  it('corrects the still-held sentence when the count lands after the dialog opened', async () => {
    world.heldCount = 3;
    world.holdCount = true;

    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel batch' }));

    const dialog = await screen.findByRole('dialog');
    // Nobody has answered yet, and the dialog says exactly that rather than a number.
    expect(
      within(dialog).getByText(
        'How many of this batch’s commands the platform still holds could not be read, so how much this stops is not known in advance.',
      ),
    ).toBeTruthy();

    // The answer lands while the operator is still deciding.
    world.countResolvers.forEach((release) => release());

    expect(
      await within(dialog).findByText(
        'The platform is still holding 3 of this batch’s commands and will stop them.',
      ),
    ).toBeTruthy();
    expect(within(dialog).queryByText(/could not be read/)).toBeNull();
  });

  it('sends nothing when the operator leaves it running', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel batch' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Leave it running' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(world.cancelCalls.length).toBe(0);
  });
});

describe('the outcome report', () => {
  // 🔴🔴 THE ONE THAT MATTERS. 300 commands are at their devices and about to actuate
  // hardware. Nothing on this screen may present them as stopped.
  it('reports commands already at their devices as still going to act, never as stopped', async () => {
    world.cancelResult = {
      cancelled: 1000,
      alreadySent: 300,
      alreadyFinished: 0,
      matched: 1300,
    };

    renderPage();
    await cancelAndConfirm();

    // What was actually stopped is the measured count alone.
    expect(
      await screen.findByText(
        '1,000 commands the platform still held were stopped and will not be delivered.',
      ),
    ).toBeTruthy();
    // 🔴 And the fleet is explicitly still acting.
    expect(
      screen.getByText(
        '300 commands had already been handed to their devices and could not be recalled. Those devices will still act on them.',
      ),
    ).toBeTruthy();
    // 🔴 THE DEFECT STATED DIRECTLY: the two must never be added into one "stopped" total.
    expect(screen.queryByText(/1,300 commands the platform still held were stopped/)).toBeNull();
    // The bucket is labelled by WHERE the commands are, not by what happened to them.
    expect(screen.getByText('Already at devices')).toBeTruthy();
  });

  // The counterweight. A panel that ALWAYS warned about in-flight commands would pass the
  // test above and cry wolf on every clean cancel.
  it('says nothing is still on its way when no command had reached a device', async () => {
    world.cancelResult = { cancelled: 8, alreadySent: 0, alreadyFinished: 2, matched: 10 };

    renderPage();
    await cancelAndConfirm();

    expect(
      await screen.findByText(
        'No command had reached a device, so nothing from this batch is still on its way to hardware.',
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/will still act on/)).toBeNull();
    expect(screen.getByText('Every command this batch still has is accounted for.')).toBeTruthy();
  });

  // 🔴 The gap between `matched` and the sum is the only evidence that commands went back
  // into the delivery queue and this cancel missed them.
  it('tells the operator to cancel again when the counts fall short of the commands found', async () => {
    world.cancelResult = { cancelled: 900, alreadySent: 60, alreadyFinished: 20, matched: 1000 };

    renderPage();
    await cancelAndConfirm();

    expect(
      await screen.findByText(
        '20 of this batch’s commands went back into the delivery queue while this ran, so this cancel did not stop them.',
      ),
    ).toBeTruthy();
    expect(screen.getByText(/Cancel again — nothing retries on your behalf/)).toBeTruthy();
    // …and the remedy is an ACTION, right there.
    expect(screen.getByRole('button', { name: 'Cancel again' })).toBeTruthy();
    // The reassuring alternative reading must NOT also be on screen.
    expect(screen.queryByText('Every command this batch still has is accounted for.')).toBeNull();
  });

  it('does not ask for a second cancel when the numbers settle', async () => {
    world.cancelResult = { cancelled: 900, alreadySent: 80, alreadyFinished: 20, matched: 1000 };

    renderPage();
    await cancelAndConfirm();

    expect(
      await screen.findByText('Every command this batch still has is accounted for.'),
    ).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Cancel again' })).toBeNull();
    expect(screen.queryByText(/went back into the delivery queue/)).toBeNull();
  });

  // 🔴 THE REMEDY SURVIVES THE STAMP. By the time this renders the batch IS cancelled, and
  // cancelling again is exactly what the service prescribes — gating the fix on the
  // condition it fixes would leave live commands running with no way to stop them.
  it('keeps offering Cancel again after the batch has been stamped cancelled', async () => {
    world.cancelResult = { cancelled: 900, alreadySent: 60, alreadyFinished: 20, matched: 1000 };
    world.afterCancel = { cancelledAt: '2026-08-12T13:00:00Z', cancelledCount: 900 };

    renderPage();
    await cancelAndConfirm();

    // The re-read landed: the batch now carries its cancellation.
    expect(await screen.findByText(/Called off on/)).toBeTruthy();
    // Nothing offers itself as a FIRST cancellation any more…
    expect(screen.queryByRole('button', { name: 'Cancel batch' })).toBeNull();
    // …and the remedy is offered twice, on purpose and from two different sources of truth:
    // this report's own shortfall, and the header's live count of what is still held. The
    // second can come back 0 while the first is on screen, so neither subsumes the other.
    expect(screen.getAllByRole('button', { name: 'Cancel again' }).length).toBe(2);
    expect(
      screen.getByText(
        'The batch’s own record keeps the first cancellation’s time and count — cancelling again stops what is left without rewriting it.',
      ),
    ).toBeTruthy();
  });

  it('cancels by the batch’s own token and re-reads the record afterwards', async () => {
    world.cancelResult = { cancelled: 3, alreadySent: 0, alreadyFinished: 0, matched: 3 };

    renderPage();
    const readsBefore = gqlMock.mock.calls.filter((c) =>
      String(c[1]).includes('query CommandBatchesByToken'),
    ).length;
    await cancelAndConfirm();

    await waitFor(() => expect(world.cancelCalls.length).toBe(1));
    expect(world.cancelCalls[0][0]).toEqual({ token: 'batch-1' });
    // The stamp, the count and every row's status are stale now.
    await waitFor(() =>
      expect(
        gqlMock.mock.calls.filter((c) => String(c[1]).includes('query CommandBatchesByToken'))
          .length,
      ).toBeGreaterThan(readsBefore),
    );
  });

  // 🔴 A PANEL, NOT A TOAST. Four numbers that need not sum are a report someone may need
  // to read twice or quote; a container that vanishes is the wrong one.
  it('renders the outcome as a persistent panel rather than a toast', async () => {
    world.cancelResult = { cancelled: 2, alreadySent: 1, alreadyFinished: 0, matched: 3 };

    renderPage();
    await cancelAndConfirm();

    expect(await screen.findByText('Cancellation result')).toBeTruthy();
    // All four counts are on screen, each under its own label.
    for (const label of ['Stopped', 'Already at devices', 'Already finished', 'Commands found']) {
      expect(screen.getByText(label)).toBeTruthy();
    }
    // Nothing about this outcome went through the transient channel.
    expect(toastMock).not.toHaveBeenCalled();
  });
});

describe('a cancel the platform never answered', () => {
  it('says what is unknown and that trying again is safe', async () => {
    world.cancelThrows = new Error('command-delivery unreachable');

    renderPage();
    await cancelAndConfirm();

    expect(await screen.findByText('command-delivery unreachable')).toBeTruthy();
    expect(screen.getByText(/Cancelling again is safe/)).toBeTruthy();
    // No report, because there is nothing to report — the four counts never arrived.
    expect(screen.queryByText('Cancellation result')).toBeNull();
  });
});
