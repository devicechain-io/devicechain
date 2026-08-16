// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real catalogs are wired in by
// importing the i18n config for its side effect, so the assertions below are on the
// CONTROL AN OPERATOR SEES rather than on a prop or a translation key.
//
// Only one seam is faked: the GraphQL transport (`gql`). The panel's real cancellability
// rule decides which rows offer Cancel — which is a NARROWER rule than terminality, since
// SENT is in flight and still cannot be called back.
//
// 🔴 WHY THIS FILE EXISTS. The panel used to keep its own copy of the terminal status
// set. When cancellation started writing CANCELLED instead of EXPIRED, that copy never
// learned the new value, so an already-cancelled command still rendered a Cancel button —
// and clicking it reported SUCCESS, because the service answers an uncancellable cancel
// by returning the row unchanged with no error. Nothing in the frontend asserted the SET
// of statuses, so it broke in total silence — and silently, in the operator's favour, is
// the worst way for it to break.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

// Spread the real module so everything except the wire stays genuine.
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The toast is the panel's only report channel for an issue attempt, so it is the seam
// the outcome assertions read. Everything else (the real i18n catalogs, the real API
// module, the real form) stays genuine.
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { GraphQLRequestError } from '@devicechain/client';

import { DeviceCommandsPanel } from './DeviceCommandsPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

const CANCEL = 'Cancel';

// One history row per status, named after the status so a row is addressable by the
// command name — which is what lets each assertion be scoped to ITS row rather than to
// "somewhere on the page".
function commandRow(status: string) {
  return {
    id: `id-${status}`,
    token: `tok-${status}`,
    deviceToken: 'therm-1',
    // Explicitly null rather than omitted: null is what the service sends for a command
    // issued one at a time, and every fixture in this file except the batch pair below is
    // one. It also keeps the cancel/issue tests as a standing control on the link — none
    // of them is wrapped in a Router, so a link rendered unconditionally would blow them up.
    batchToken: null as string | null,
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

// ── Issue-outcome fixtures ─────────────────────────────────────────────────
//
// The panel talks to two services and, within command-delivery, to two operations. The
// answers dispatch on the DOCUMENT rather than the service so the history query and the
// createCommand mutation can be answered differently in one render.

type IssueOutcome =
  | { kind: 'created' }
  | { kind: 'rejected'; code: string; reason: string }
  | { kind: 'failed'; error: Error };

function respondToIssue(outcome: IssueOutcome) {
  gqlMock.mockImplementation((service: string, document: unknown) => {
    if (String(document).includes('mutation CreateCommand')) {
      if (outcome.kind === 'failed') return Promise.reject(outcome.error);
      if (outcome.kind === 'rejected') {
        return Promise.resolve({
          createCommand: {
            command: null,
            rejection: { code: outcome.code, reason: outcome.reason },
          },
        });
      }
      return Promise.resolve({
        createCommand: {
          command: { id: 'id-new', token: 'tok-new', status: 'QUEUED' },
          rejection: null,
        },
      });
    }
    if (service === 'command-delivery') {
      return Promise.resolve({
        commands: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } },
      });
    }
    return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
  });
}

// Fill the free-text command name and press Issue command — the unconstrained path, so
// the assertions are about the OUTCOME rather than about vocabulary handling.
async function issueCommand(name = 'reboot') {
  const input = await screen.findByPlaceholderText('e.g. reboot');
  fireEvent.change(input, { target: { value: name } });
  fireEvent.click(screen.getByRole('button', { name: 'Issue command' }));
  return input as HTMLInputElement;
}

// The Cancel control inside the row for a given status, or null when none is offered.
function cancelControlFor(status: string): HTMLElement | null {
  const row = screen.getByText(`cmd-${status}`).closest('tr');
  if (!row) throw new Error(`no row rendered for ${status}`);
  return within(row).queryByRole('button', { name: CANCEL });
}

describe('DeviceCommandsPanel cancel control', () => {
  // 🔴🔴 THE ONE THAT MATTERS, and what it asserts is that CANCELLABLE ≠ NON-TERMINAL.
  // CANCELLED is terminal: a second cancellation is not refused, it is ACCEPTED AND
  // IGNORED — the service returns the row unchanged — so offering the button there buys
  // the operator nothing and costs them a toast about work that never happened. SENT is
  // NOT terminal and still must not offer it — the command is already at the device, and
  // the service will not call back something it has handed over. The three cancellable
  // states are asserted in the SAME render as the counterweight: a panel that had simply
  // stopped offering Cancel altogether would satisfy every negative assertion perfectly
  // and fail here.
  it('offers Cancel only while a command can still be cancelled', async () => {
    respondWith([
      'QUEUED',
      'HELD',
      'SENT',
      'PARKED',
      'SUCCESSFUL',
      'FAILED',
      'TIMEOUT',
      'EXPIRED',
      'CANCELLED',
    ]);

    render(<DeviceCommandsPanel deviceToken="therm-1" />);

    // The history has painted once the first row is on screen.
    expect(await screen.findByText('cmd-QUEUED')).toBeTruthy();

    for (const live of ['QUEUED', 'HELD', 'PARKED']) {
      expect(cancelControlFor(live), `${live} has not reached the device and must offer Cancel`).toBeTruthy();
    }
    // 🔴 SENT sits with the terminals here despite being in flight. Gating the column on
    // !isTerminalCommandStatus — which is what this panel used to do — puts a live button
    // on this row whose click the server answers with a cheerful 200 and no change at all.
    for (const uncancellable of ['SENT', 'SUCCESSFUL', 'FAILED', 'TIMEOUT', 'EXPIRED', 'CANCELLED']) {
      expect(
        cancelControlFor(uncancellable),
        `${uncancellable} cannot be cancelled and must NOT offer Cancel`,
      ).toBeNull();
    }
    // Exactly three, so a stray button elsewhere in an uncancellable row can't hide behind
    // the per-row queries above.
    expect(screen.getAllByRole('button', { name: CANCEL })).toHaveLength(3);
  });

  // HELD and PARKED are the states most likely to be mistaken for dead commands: both can
  // sit for days waiting for an absent device. They are displayed like the other in-flight
  // states and they keep their Cancel control — an operator whose command is stuck behind
  // an offline machine needs exactly that button.
  it('shows a waiting command as still in flight rather than as an outcome', async () => {
    respondWith(['HELD', 'PARKED', 'EXPIRED']);

    render(<DeviceCommandsPanel deviceToken="therm-1" />);

    // The status is rendered as the raw uppercase value the service sends — there is no
    // translation catalog for statuses, and this test pins that the new ones are shown
    // exactly like the ones that came before.
    expect(await screen.findByText('HELD')).toBeTruthy();
    expect(screen.getByText('PARKED')).toBeTruthy();
    expect(cancelControlFor('HELD')).toBeTruthy();
    expect(cancelControlFor('PARKED')).toBeTruthy();
    expect(cancelControlFor('EXPIRED')).toBeNull();
  });
});

// ── Cancel-outcome fixtures ────────────────────────────────────────────────
//
// 🔴🔴 THE SERVICE NEVER REFUSES A CANCEL. It gates cancellation on a positive list of
// statuses and, for anything outside that list, SUCCEEDS AND RETURNS THE COMMAND
// UNCHANGED — no error, nothing to catch. The only evidence that a cancellation actually
// happened is therefore the STATUS THAT COMES BACK, so the fixture lets the mutation
// answer with a status of its own choosing while the history row stays QUEUED (which is
// what put the button on screen in the first place).
function respondToCancel(returnedStatus: string) {
  gqlMock.mockImplementation((service: string, document: unknown) => {
    if (String(document).includes('mutation CancelCommand')) {
      return Promise.resolve({
        cancelCommand: { id: 'id-QUEUED', token: 'tok-QUEUED', status: returnedStatus },
      });
    }
    if (service === 'command-delivery') {
      return Promise.resolve({
        commands: {
          results: [commandRow('QUEUED')],
          pagination: { pageStart: 0, pageEnd: 1, totalRecords: 1 },
        },
      });
    }
    return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
  });
}

async function clickCancel() {
  await screen.findByText('cmd-QUEUED');
  const button = cancelControlFor('QUEUED');
  if (!button) throw new Error('the QUEUED row offered no Cancel button');
  fireEvent.click(button);
}

describe('DeviceCommandsPanel cancel outcomes', () => {
  it('reports a cancellation only when the command came back CANCELLED', async () => {
    respondToCancel('CANCELLED');

    render(<DeviceCommandsPanel deviceToken="therm-1" />);
    await clickCancel();

    await waitFor(() => expect(toastMock).toHaveBeenCalled());
    expect(toastMock).toHaveBeenCalledWith('Command “cmd-QUEUED” cancelled');
    // No variant ⇒ success. The failure arm below asserts 'error', so this is the half
    // that keeps "always report failure" from passing the pair.
    expect(toastMock.mock.calls[0]).toHaveLength(1);
  });

  // 🔴🔴 THE ONE THAT MATTERS. The button was offered against a QUEUED row, but between
  // that read and the click the platform dispatched the command — and the mutation still
  // RESOLVES, carrying the untouched SENT row. A panel that reports off `await` alone
  // passes every other test in this file and tells the operator, wrongly and cheerfully,
  // that a command now sitting at the device was called off.
  it('does not claim success when the command was dispatched before the click', async () => {
    respondToCancel('SENT');

    render(<DeviceCommandsPanel deviceToken="therm-1" />);
    await clickCancel();

    await waitFor(() => expect(toastMock).toHaveBeenCalled());
    const [message, variant] = toastMock.mock.calls[0];
    expect(variant).toBe('error');
    // The status that came back is the useful part, so the operator is told it by name.
    expect(message).toContain('SENT');
    expect(message).toContain('wasn’t cancelled');
    // …and it is emphatically not the success toast.
    expect(message).not.toBe('Command “cmd-QUEUED” cancelled');
  });

  // The same trap with a terminal answer: the device replied first. Still a success on the
  // wire, still not a cancellation. Asserted separately from SENT so a check that special-
  // cased one in-flight status rather than testing for CANCELLED is caught.
  it('does not claim success when the command had already finished', async () => {
    respondToCancel('SUCCESSFUL');

    render(<DeviceCommandsPanel deviceToken="therm-1" />);
    await clickCancel();

    await waitFor(() => expect(toastMock).toHaveBeenCalled());
    const [message, variant] = toastMock.mock.calls[0];
    expect(variant).toBe('error');
    expect(message).toContain('SUCCESSFUL');
    expect(message).not.toBe('Command “cmd-QUEUED” cancelled');
  });
});

// 🔴🔴 THE THREE ANSWERS AN ENQUEUE CAN GIVE, and the point of the change is that they
// are now distinguishable. createCommand used to return the command or throw, so every
// refusal — an unknown device, a command the profile never published, a payload that
// violates its schema, a tenant at its held-command ceiling — reached the operator as
// whatever error string happened to be in flight, styled exactly like the service being
// down. A rejection is a DECIDED verdict about this command and its reason is the useful
// part; a thrown error is the platform failing to answer and says nothing about the
// command at all.
describe('DeviceCommandsPanel issue outcomes', () => {
  it('reports a successful enqueue and clears the form', async () => {
    respondToIssue({ kind: 'created' });

    render(<DeviceCommandsPanel deviceToken="therm-1" />);
    const input = await issueCommand();

    await waitFor(() => expect(toastMock).toHaveBeenCalled());
    expect(toastMock).toHaveBeenCalledWith('Command “reboot” issued');
    // The success toast carries no variant — the error arms below assert 'error'.
    expect(toastMock.mock.calls[0]).toHaveLength(1);
    await waitFor(() => expect(input.value).toBe(''));
  });

  it('shows the server’s reason when the enqueue is refused', async () => {
    respondToIssue({
      kind: 'rejected',
      code: 'COMMAND_NOT_IN_VOCABULARY',
      reason: 'cannot enqueue command: command "reboot" is not in the published vocabulary for device "therm-1"',
    });

    render(<DeviceCommandsPanel deviceToken="therm-1" />);
    const input = await issueCommand();

    await waitFor(() => expect(toastMock).toHaveBeenCalled());
    const [message, variant] = toastMock.mock.calls[0];
    expect(variant).toBe('error');
    // The reason is shown VERBATIM — that is the whole value of the new arm.
    expect(message).toContain('is not in the published vocabulary for device "therm-1"');
    // ...and it is not dressed up as the platform failing.
    expect(message).not.toContain('Couldn’t issue the command');
    // The typed command survives, because the operator's next move is to correct it.
    expect(input.value).toBe('reboot');
  });

  it('shows a generic failure — never the platform’s error text — when the enqueue can’t be decided', async () => {
    respondToIssue({
      kind: 'failed',
      error: new GraphQLRequestError(
        'ERROR: relation "commands" does not exist (SQLSTATE 42P01)',
        500,
      ),
    });

    render(<DeviceCommandsPanel deviceToken="therm-1" />);
    await issueCommand();

    await waitFor(() => expect(toastMock).toHaveBeenCalled());
    const [message, variant] = toastMock.mock.calls[0];
    expect(variant).toBe('error');
    expect(message).toBe('Couldn’t issue the command. Try again.');
    // 🔴 The server's text names in-cluster machinery and asserts nothing about the
    // command. It must not reach the operator, and it must not read as a verdict.
    expect(message).not.toContain('SQLSTATE');
    expect(message).not.toContain('relation "commands"');
    expect(message).not.toContain('refused');
  });
});

// ── Batch provenance ───────────────────────────────────────────────────────
//
// 🔴🔴 WHY THIS PAIR EXISTS. A command a fleet write minted is indistinguishable from a
// one-off on every field this table already showed: same command key, same device, same
// payload, same lifecycle. `batchToken` is the ONLY thing that says where it came from,
// so the link has to be gated on that field and on nothing else — and the negative case
// is not a formality. A link rendered unconditionally satisfies the positive test
// perfectly, and would tell an operator that a command they issued by hand was part of a
// fleet write, pointing at a batch page that does not exist.
//
// These renders are wrapped in a Router because a <Link> needs one. That matters for the
// negative test in particular: without the wrapper it would "pass" by throwing, which
// proves nothing about whether a link was rendered.

const BATCH_LINK = 'Part of a fleet write';

function respondWithBatchToken(batchToken: string | null) {
  gqlMock.mockImplementation((service: string) => {
    if (service === 'command-delivery') {
      return Promise.resolve({
        commands: {
          results: [{ ...commandRow('QUEUED'), batchToken }],
          pagination: { pageStart: 0, pageEnd: 1, totalRecords: 1 },
        },
      });
    }
    return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
  });
}

function renderRouted() {
  return render(
    <MemoryRouter>
      <DeviceCommandsPanel deviceToken="therm-1" />
    </MemoryRouter>,
  );
}

// The command's own row, so a link found anywhere else on the panel cannot stand in for
// one on the row that actually came from a batch.
async function queuedRow(): Promise<HTMLElement> {
  const row = (await screen.findByText('cmd-QUEUED')).closest('tr');
  if (!row) throw new Error('no row rendered for the QUEUED command');
  return row as HTMLElement;
}

describe('DeviceCommandsPanel batch provenance', () => {
  it('links a batch-minted command to the fleet write that created it', async () => {
    respondWithBatchToken('batch-77');

    renderRouted();

    const link = within(await queuedRow()).getByRole('link', { name: BATCH_LINK });
    // The route registered in App.tsx, not a path this component invented.
    expect(link.getAttribute('href')).toBe('/command-batches/batch-77');
  });

  // 🔴 THE CONTROL. The assertion above passes just as well if the link is always there.
  it('shows no batch link on a command issued one at a time', async () => {
    respondWithBatchToken(null);

    renderRouted();

    // The row painted — so "no link" is a fact about a rendered row, not about an empty
    // table that never got as far as deciding.
    const row = await queuedRow();
    expect(within(row).queryByRole('link')).toBeNull();
    expect(screen.queryByText(BATCH_LINK)).toBeNull();
    // Nothing else on the panel links out either, which is what makes the scoped query
    // above meaningful.
    expect(screen.queryAllByRole('link')).toHaveLength(0);
  });

  // A batch token is minted by whoever fired the batch — the console uses a UUID, but an
  // SDK caller supplies its own string — so it is arbitrary text and has to be escaped
  // before it becomes a path segment. Unescaped, a token carrying a slash silently
  // navigates somewhere else entirely.
  it('escapes the token before putting it in the path', async () => {
    respondWithBatchToken('night/shift 1');

    renderRouted();

    const link = within(await queuedRow()).getByRole('link', { name: BATCH_LINK });
    expect(link.getAttribute('href')).toBe('/command-batches/night%2Fshift%201');
  });
});
