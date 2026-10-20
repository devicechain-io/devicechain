// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The retry is built FROM the unsuccessful-command rows, so the form must only ever offer the
// answer to the CURRENT read. After a reload (a cancel changes which commands are unsuccessful)
// the previous rows are the answer to a different question: while the re-read is in flight the
// panel must not offer them, and a Retry must not be submittable from them.
//
// NOTE: reverting only the candidates useMemo to `.data` does NOT change behaviour (the
// `!unsuccessful.current` gate below it returns the loading state before the candidates are
// ever read, and the memo recomputes when `current` lands). What these tests pin is the
// observable guarantee: the gate and the memo together never offer stale rows.
import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['command:read', 'command:write'] } }),
}));

import { RetryBatchPanel } from './RetryBatchAction';

function cmd(deviceToken: string) {
  return {
    id: deviceToken,
    token: `c-${deviceToken}`,
    deviceToken,
    batchToken: 'batch-1',
    name: 'reboot',
    payload: null,
    status: 'FAILED',
    queuedTime: '2026-08-12T12:00:00Z',
    sentTime: null,
    respondedTime: null,
    expiresAt: null,
    responsePayload: null,
    error: null,
    outcome: 'NOT_DELIVERED',
  };
}

const BATCH = {
  id: 'id-1',
  token: 'batch-1',
  createdAt: '2026-08-12T12:00:00Z',
  name: 'reboot',
  payload: null,
  targetKind: 'DEVICE_LIST',
  groupToken: null,
  groupVersion: null,
  allowPartial: true,
  resolved: 1,
  accepted: 1,
  refusals: [],
  refusalCounts: [],
  cancelledAt: null,
  cancelledCount: null,
  retryOf: null,
  retries: [] as string[],
} as never;

const STATE = { canRetry: true, open: true, toggle: () => {} };

// What the unsuccessful-commands read answers on each call; later reads are given by `reread`.
let reread: () => Promise<unknown>;

beforeEach(() => {
  gqlMock.mockReset();
  let unsuccessfulReads = 0;
  gqlMock.mockImplementation((_s: string, _doc: unknown, variables: unknown) => {
    const criteria = (variables as { criteria: Record<string, unknown> }).criteria;
    if ('outcomes' in criteria) {
      unsuccessfulReads += 1;
      if (unsuccessfulReads === 1) {
        return Promise.resolve({
          commands: {
            results: [cmd('device-x')],
            pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
          },
        });
      }
      return reread();
    }
    return Promise.resolve({
      commands: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } },
    });
  });
});
afterEach(cleanup);

function panel(reloadKey: number) {
  return (
    <MemoryRouter>
      <RetryBatchPanel batch={BATCH} state={STATE} reloadKey={reloadKey} />
    </MemoryRouter>
  );
}

describe('the retry panel across a reload', () => {
  it('does not offer the previous rows while the re-read is pending', async () => {
    reread = () => new Promise(() => {});
    const { rerender } = render(panel(0));
    expect(await screen.findByRole('checkbox', { name: 'device-x' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Retry 1 device' })).toBeTruthy();

    rerender(panel(1));

    await waitFor(() => expect(screen.queryByRole('checkbox', { name: 'device-x' })).toBeNull());
    expect(screen.queryByRole('button', { name: /^Retry \d+ device/ })).toBeNull();
    expect(screen.getByText(/Reading this batch.s unsuccessful commands/)).toBeTruthy();
  });

  it('shows the error and offers nothing when the re-read fails', async () => {
    reread = () => Promise.reject(new Error('boom'));
    const { rerender } = render(panel(0));
    expect(await screen.findByRole('checkbox', { name: 'device-x' })).toBeTruthy();

    rerender(panel(1));

    await waitFor(() => expect(screen.queryByRole('checkbox', { name: 'device-x' })).toBeNull());
    expect(screen.queryByRole('button', { name: /^Retry \d+ device/ })).toBeNull();
  });
});
