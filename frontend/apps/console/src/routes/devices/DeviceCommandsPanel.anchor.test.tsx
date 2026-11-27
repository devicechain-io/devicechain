// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A link such as the approvals inbox's (`/devices/<token>?tab=commands#command-<token>`) names
// one command. The panel loads its history AFTER the hash is read and shows only the first page,
// so the row must be found once the list has loaded, and a command on a later page must be read
// by token. Only the GraphQL transport is faked.
import '@/i18n/config';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('./HeldWhileDisabledNotice', () => ({ HeldWhileDisabledNotice: () => null }));

import { COMMAND_HIGHLIGHT_MS, DeviceCommandsPanel, commandAnchor } from './DeviceCommandsPanel';

function row(token: string, status = 'SUCCEEDED', deviceToken = 'therm-1') {
  return {
    id: `id-${token}`, token, deviceToken, batchToken: null, name: `cmd-${token}`, payload: null, status,
    queuedTime: '2026-08-12T12:00:00Z', sentTime: null, respondedTime: null, expiresAt: null,
    dispatchAfter: null, maintenanceWindowToken: null, responsePayload: null, error: null, outcome: null,
  };
}

// The command-delivery answers: the history is the first page; `byToken` is what a lookup by token
// returns (an unknown token comes back as an empty list, not an error).
function serve(opts: { page: ReturnType<typeof row>[]; byToken?: ReturnType<typeof row>[]; byTokenError?: Error }) {
  gqlMock.mockImplementation((service: string, document: unknown) => {
    if (String(document).includes('query CommandByToken')) {
      return opts.byTokenError ? Promise.reject(opts.byTokenError) : Promise.resolve({ commandsByToken: opts.byToken ?? [] });
    }
    if (service === 'command-delivery') {
      return Promise.resolve({
        commands: { results: opts.page, pagination: { pageStart: 1, pageEnd: opts.page.length, totalRecords: opts.page.length + 40 } },
      });
    }
    return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
  });
}
const lookups = () => gqlMock.mock.calls.filter(([, doc]) => String(doc).includes('query CommandByToken'));

const scrolled: string[] = [];
beforeEach(() => {
  gqlMock.mockReset();
  scrolled.length = 0;
  Element.prototype.scrollIntoView = vi.fn(function (this: Element) { scrolled.push(this.id); });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
});

const panel = (anchorToken?: string) => (
  <DeviceCommandsPanel deviceToken="therm-1" canIssue={false} canCancel anchorToken={anchorToken} />
);
const rowOf = (name: string) => screen.getByText(name).closest('tr') as HTMLElement;

describe('a command link scrolls to its row', () => {
  it('on the first page, after the list has loaded', async () => {
    serve({ page: [row('c-1'), row('c-2'), row('c-3')] });
    render(panel('c-2'));
    await screen.findByText('cmd-c-2');
    await waitFor(() => expect(scrolled).toEqual(['command-c-2']));
    expect(rowOf('cmd-c-2').className).toContain('bg-primary/10');
    expect(rowOf('cmd-c-1').className).not.toContain('bg-primary/10');
    expect(lookups()).toHaveLength(0);
  });

  it('once, however many times the list re-reads', async () => {
    serve({ page: [row('c-1', 'QUEUED'), row('c-2')] });
    const view = render(panel('c-2'));
    await waitFor(() => expect(scrolled).toEqual(['command-c-2']));
    view.rerender(panel('c-2'));
    await act(async () => { await Promise.resolve(); });
    expect(scrolled).toEqual(['command-c-2']);
  });

  it('and the highlight fades', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve({ page: [row('c-1')] });
    render(panel('c-1'));
    await waitFor(() => expect(rowOf('cmd-c-1').className).toContain('bg-primary/10'));
    // The class is in the DOM at the commit, but the fade is scheduled by the effect that runs
    // after it (the one that scrolls), and React flushes that effect in a task of its own. Under
    // load the clock can be advanced before that task runs, past a fade that does not exist yet.
    // The scroll is the effect having run, so the fade's timer is set.
    await waitFor(() => expect(scrolled).toEqual(['command-c-1']));
    await act(async () => { vi.advanceTimersByTime(COMMAND_HIGHLIGHT_MS + 50); });
    expect(rowOf('cmd-c-1').className).not.toContain('bg-primary/10');
  });

  it('reads a command that is not on the loaded page by token, and pins it above the history', async () => {
    serve({ page: [row('c-1'), row('c-2')], byToken: [row('old-9', 'QUEUED')] });
    render(panel('old-9'));
    await screen.findByText('cmd-old-9');
    await waitFor(() => expect(scrolled).toEqual(['command-old-9']));
    expect(lookups()).toHaveLength(1);
    expect(lookups()[0][2]).toEqual({ tokens: ['old-9'] });
    const rows = screen.getAllByRole('row').slice(1);
    expect(rows[0].id).toBe('command-old-9');
    expect(rowOf('cmd-old-9').className).toContain('bg-primary/10');
    expect(screen.getByRole('status').textContent).toContain('old-9');
    // Read once and not polled, so it must not offer Cancel on a status that may have moved.
    expect(within(rowOf('cmd-old-9')).queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('says so, without throwing, when the command no longer exists', async () => {
    serve({ page: [row('c-1')], byToken: [] });
    render(panel('missing'));
    await screen.findByText(/not in this device's history/);
    expect(scrolled).toEqual([]);
    expect(screen.getByText('cmd-c-1')).toBeTruthy();
  });

  it("says so when the command belongs to another device", async () => {
    serve({ page: [row('c-1')], byToken: [row('theirs', 'SUCCEEDED', 'other-device')] });
    render(panel('theirs'));
    await screen.findByText(/not in this device's history/);
    expect(screen.queryByText('cmd-theirs')).toBeNull();
    expect(scrolled).toEqual([]);
  });

  it('says so when the lookup fails', async () => {
    serve({ page: [row('c-1')], byTokenError: new Error('boom') });
    render(panel('old-9'));
    await screen.findByText('The linked command could not be loaded.');
    expect(scrolled).toEqual([]);
  });

  it('does nothing without a link', async () => {
    serve({ page: [row('c-1')] });
    render(panel());
    await screen.findByText('cmd-c-1');
    expect(scrolled).toEqual([]);
    expect(lookups()).toHaveLength(0);
    expect(rowOf('cmd-c-1').className).not.toContain('bg-primary/10');
  });
});

describe('commandAnchor', () => {
  it('reads the token a `#command-<token>` fragment names, decoded', () => {
    expect(commandAnchor('#command-abc')).toBe('abc');
    expect(commandAnchor('#command-a%2Fb%20c')).toBe('a/b c');
  });
  it('names none for anything else', () => {
    expect(commandAnchor('')).toBeUndefined();
    expect(commandAnchor('#command-')).toBeUndefined();
    expect(commandAnchor('#alarm-3')).toBeUndefined();
    expect(commandAnchor('#command-%E0%A4%A')).toBeUndefined();
  });
});
