// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the REAL i18n catalogs, the real API module and the real query
// documents; only the GraphQL transport and the auth claims are faked.
//
// 🔴 WHAT THESE PIN: a target that may already have acted (outcome UNKNOWN) is never
// preselected, cannot be selected until the operator ticks the acknowledgement, and is sent
// with `includeUnknown` only then; what is sent is the STORED outcome's grouping, so error
// prose that says the opposite changes nothing; in-flight commands are not offered.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, authorities } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  authorities: { value: ['command:read', 'command:write'] as string[] },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import CommandBatchDetailPage from './CommandBatchDetailPage';

function cmd(deviceToken: string, outcome: string | null, error: string | null = null) {
  return {
    id: deviceToken,
    token: `c-${deviceToken}`,
    deviceToken,
    batchToken: 'batch-1',
    name: 'reboot',
    payload: null,
    status: outcome === 'SUCCEEDED' ? 'SUCCESSFUL' : 'FAILED',
    queuedTime: '2026-08-12T12:00:00Z',
    sentTime: null,
    respondedTime: null,
    expiresAt: null,
    responsePayload: null,
    error,
    outcome,
  };
}

const world = {
  batch: {} as Record<string, unknown>,
  unsuccessful: [] as ReturnType<typeof cmd>[],
  retryResult: null as unknown,
  retryCalls: [] as Record<string, unknown>[],
  unsuccessfulCriteria: [] as Record<string, unknown>[],
};

function batch(over: Record<string, unknown> = {}) {
  return {
    id: 'id-1',
    token: 'batch-1',
    createdAt: '2026-08-12T12:00:00Z',
    name: 'reboot',
    payload: null,
    targetKind: 'DEVICE_LIST',
    groupToken: null,
    groupVersion: null,
    allowPartial: true,
    resolved: 7,
    accepted: 6,
    refusals: [{ deviceToken: 'refused-1', code: 'DEVICE_DISABLED', reason: 'device is disabled' }],
    refusalCounts: [{ code: 'DEVICE_DISABLED', count: 1 }],
    cancelledAt: null,
    cancelledCount: null,
    retryOf: null,
    retries: [] as string[],
    ...over,
  };
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  authorities.value = ['command:read', 'command:write'];
  world.batch = batch();
  world.unsuccessful = [
    // The error text deliberately contradicts the stored outcome.
    cmd('never-1', 'NOT_DELIVERED', 'device returned 4.04'),
    cmd('rejected-1', 'REJECTED', 'It was never delivered'),
    cmd('unknown-1', 'UNKNOWN', 'device did not respond (timeout)'),
  ];
  world.retryResult = {
    retryCommandBatch: {
      batch: { id: 'r', token: 'retry-1', retryOf: 'batch-1', resolved: 2, accepted: 2 },
      rejection: null,
    },
  };
  world.retryCalls = [];
  world.unsuccessfulCriteria = [];
  gqlMock.mockImplementation((_s: string, document: unknown, variables: unknown) => {
    const doc = String(document);
    if (doc.includes('mutation RetryCommandBatch')) {
      world.retryCalls.push((variables as { request: Record<string, unknown> }).request);
      return Promise.resolve(world.retryResult);
    }
    if (doc.includes('query CommandBatchesByToken')) {
      return Promise.resolve({ commandBatchesByToken: [world.batch] });
    }
    const criteria = (variables as { criteria: Record<string, unknown> }).criteria;
    if ('outcomes' in criteria) {
      world.unsuccessfulCriteria.push(criteria);
      return Promise.resolve({
        commands: {
          results: world.unsuccessful,
          pagination: { pageStart: 1, pageEnd: 3, totalRecords: world.unsuccessful.length },
        },
      });
    }
    return Promise.resolve({
      commands: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 2 } },
    });
  });
});

function renderPage() {
  render(
    <ConfirmProvider>
      <MemoryRouter initialEntries={['/command-batches/batch-1']}>
        <Routes>
          <Route path="/command-batches/:token" element={<CommandBatchDetailPage />} />
          <Route path="/command-batches/retry-1" element={<div>retry page</div>} />
        </Routes>
      </MemoryRouter>
    </ConfirmProvider>,
  );
}

async function openPanel() {
  fireEvent.click(await screen.findByRole('button', { name: /Retry…/ }));
  await screen.findByText('Retry unsuccessful devices');
}

describe('the retry action', () => {
  it('is offered only with command:write', async () => {
    authorities.value = ['command:read'];
    renderPage();
    expect(await screen.findByText('Summary')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Retry…/ })).toBeNull();
  });

  it('selects by stored outcome class and asks for the unfinished-by-outcome classes only', async () => {
    renderPage();
    await openPanel();

    expect(world.unsuccessfulCriteria[0]?.outcomes).toEqual(['NOT_DELIVERED', 'REJECTED', 'UNKNOWN']);
    // Proven-not-acted classes are preselected; the button counts exactly those two.
    expect(await screen.findByRole('button', { name: 'Retry 2 devices' })).toBeTruthy();
    expect((screen.getByRole('checkbox', { name: 'never-1' }) as HTMLButtonElement).getAttribute('aria-checked')).toBe('true');
    expect((screen.getByRole('checkbox', { name: 'rejected-1' }) as HTMLButtonElement).getAttribute('aria-checked')).toBe('true');
    // The unknown device is listed but locked and unselected.
    const unknown = screen.getByRole('checkbox', { name: 'unknown-1' }) as HTMLButtonElement;
    expect(unknown.disabled).toBe(true);
    expect(unknown.getAttribute('aria-checked')).toBe('false');
    // The completely-recorded refusal is offered, not preselected.
    expect(screen.getByRole('checkbox', { name: 'refused-1' }).getAttribute('aria-checked')).toBe('false');
    // In-flight commands are counted, never offered.
    expect(screen.getByText(/2 commands are still in flight/)).toBeTruthy();
  });

  it('sends the preselected devices without includeUnknown and follows the link to the retry', async () => {
    renderPage();
    await openPanel();

    fireEvent.click(await screen.findByRole('button', { name: 'Retry 2 devices' }));

    await waitFor(() => expect(world.retryCalls).toHaveLength(1));
    const req = world.retryCalls[0];
    expect(req.of).toBe('batch-1');
    expect(req.deviceTokens).toEqual(['never-1', 'rejected-1']);
    expect(req.includeUnknown).toBe(false);
    expect(req.token).not.toBe('batch-1');
    expect(await screen.findByText('retry page')).toBeTruthy();
  });

  it('needs the explicit acknowledgement before an unknown-outcome device can be sent', async () => {
    renderPage();
    await openPanel();

    // Ticking the acknowledgement unlocks the class, but selects nothing by itself.
    fireEvent.click(await screen.findByRole('checkbox', { name: /I understand these devices may already/ }));
    const unknown = screen.getByRole('checkbox', { name: 'unknown-1' }) as HTMLButtonElement;
    expect(unknown.disabled).toBe(false);
    expect(unknown.getAttribute('aria-checked')).toBe('false');
    expect(screen.getByRole('button', { name: 'Retry 2 devices' })).toBeTruthy();

    fireEvent.click(unknown);
    fireEvent.click(await screen.findByRole('button', { name: 'Retry 3 devices' }));

    await waitFor(() => expect(world.retryCalls).toHaveLength(1));
    expect(world.retryCalls[0].deviceTokens).toEqual(['never-1', 'rejected-1', 'unknown-1']);
    expect(world.retryCalls[0].includeUnknown).toBe(true);
  });

  it('drops a selected unknown device again if the acknowledgement is withdrawn', async () => {
    renderPage();
    await openPanel();

    const ack = await screen.findByRole('checkbox', { name: /I understand these devices may already/ });
    fireEvent.click(ack);
    fireEvent.click(screen.getByRole('checkbox', { name: 'unknown-1' }));
    expect(screen.getByRole('button', { name: 'Retry 3 devices' })).toBeTruthy();
    fireEvent.click(ack);

    fireEvent.click(await screen.findByRole('button', { name: 'Retry 2 devices' }));
    await waitFor(() => expect(world.retryCalls).toHaveLength(1));
    expect(world.retryCalls[0].includeUnknown).toBe(false);
    expect(world.retryCalls[0].deviceTokens).not.toContain('unknown-1');
  });

  it('shows a refusal with its code and the devices it names, and stays on the page', async () => {
    world.retryResult = {
      retryCommandBatch: {
        batch: null,
        rejection: {
          code: 'RETRY_NOT_ELIGIBLE',
          reason: '1 of 2 named devices cannot be retried',
          resolved: 2,
          refusals: [{ deviceToken: 'never-1', code: 'RETRY_NOT_ELIGIBLE', reason: 'in flight' }],
        },
      },
    };
    renderPage();
    await openPanel();

    fireEvent.click(await screen.findByRole('button', { name: 'Retry 2 devices' }));

    expect(await screen.findByText(/The retry was refused \(RETRY_NOT_ELIGIBLE\)/)).toBeTruthy();
    expect(screen.queryByText('retry page')).toBeNull();
  });
});

describe('the link between a batch and its retries', () => {
  it('links a retry back to its original and an original to its retries', async () => {
    world.batch = batch({ retryOf: 'orig-0', retries: ['later-1', 'later-2'] });
    renderPage();

    const original = await screen.findByRole('link', { name: 'orig-0' });
    expect(original.getAttribute('href')).toBe('/command-batches/orig-0');
    expect(screen.getByRole('link', { name: 'later-1' }).getAttribute('href')).toBe(
      '/command-batches/later-1',
    );
    expect(screen.getByRole('link', { name: 'later-2' })).toBeTruthy();
  });
});
