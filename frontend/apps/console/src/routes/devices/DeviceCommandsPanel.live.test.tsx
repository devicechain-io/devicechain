// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The history table follows delivery status on its own. There is no command-status
// subscription, so the panel polls while any row is non-terminal and stops once every row
// has settled. Only the GraphQL transport is faked; the real panel, hook and catalogs run.
import '@/i18n/config';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('./HeldWhileDisabledNotice', () => ({ HeldWhileDisabledNotice: () => null }));

import { COMMAND_POLL_MS, DeviceCommandsPanel } from './DeviceCommandsPanel';

beforeEach(() => {
  vi.useFakeTimers();
  gqlMock.mockReset();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function row(status: string, deviceToken = 'therm-1') {
  return {
    id: 'id-1',
    token: 'tok-1',
    deviceToken,
    batchToken: null,
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

// Answers the history read from a script of statuses, one per read; the last repeats.
function scripted(statuses: string[]) {
  let reads = 0;
  gqlMock.mockImplementation((service: string) => {
    if (service === 'command-delivery') {
      const status = statuses[Math.min(reads, statuses.length - 1)];
      reads += 1;
      return Promise.resolve({
        commands: { results: [row(status)], pagination: { pageStart: 0, pageEnd: 1, totalRecords: 1 } },
      });
    }
    return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
  });
  return () => reads;
}

// The status badge lives in the row; 'Queued' is also a column header, so read the row.
const rowText = () => screen.getByText('reboot').closest('tr')?.textContent ?? '';

const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0); });
const tick = () => act(async () => { await vi.advanceTimersByTimeAsync(COMMAND_POLL_MS); });

describe('DeviceCommandsPanel live status', () => {
  it('follows Queued, Sent, Successful without a page refetch, then stops polling', async () => {
    const reads = scripted(['QUEUED', 'SENT', 'SUCCESSFUL']);
    render(<DeviceCommandsPanel deviceToken="therm-1" canIssue={false} canCancel={false} />);
    await settle();
    expect(rowText()).toContain('Queued');

    await tick();
    expect(rowText()).toContain('Sent');
    expect(rowText()).not.toContain('Queued');

    await tick();
    expect(rowText()).toContain('Successful');

    // Every row is terminal: no further reads, however long we wait.
    const settledReads = reads();
    await act(async () => { await vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 5); });
    expect(reads()).toBe(settledReads);
  });

  it('does not poll a history that is already terminal', async () => {
    const reads = scripted(['SUCCESSFUL']);
    render(<DeviceCommandsPanel deviceToken="therm-1" canIssue={false} canCancel={false} />);
    await settle();
    await act(async () => { await vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 5); });
    expect(reads()).toBe(1);
  });

  it('keeps the table mounted while a poll is still in flight instead of flashing the loading state', async () => {
    let reads = 0;
    gqlMock.mockImplementation((service: string) => {
      if (service !== 'command-delivery') {
        return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
      }
      reads += 1;
      // The first read answers; every later one hangs, so the poll is observed mid-flight.
      if (reads > 1) return new Promise(() => {});
      return Promise.resolve({
        commands: { results: [row('SENT')], pagination: { pageStart: 0, pageEnd: 1, totalRecords: 1 } },
      });
    });
    render(<DeviceCommandsPanel deviceToken="therm-1" canIssue={false} canCancel={false} />);
    await settle();
    expect(screen.getByText('reboot')).toBeTruthy();
    await tick();
    expect(reads).toBe(2);
    expect(screen.getByText('reboot')).toBeTruthy();
  });

  it('keeps polling a status this build does not recognise', async () => {
    const reads = scripted(['IN_THE_FUTURE', 'IN_THE_FUTURE']);
    render(<DeviceCommandsPanel deviceToken="therm-1" canIssue={false} canCancel={false} />);
    await settle();
    await tick();
    expect(reads()).toBeGreaterThan(1);
  });
});
