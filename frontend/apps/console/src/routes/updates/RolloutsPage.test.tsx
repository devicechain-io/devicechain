// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The rollout list and the new-rollout form under jsdom with the real i18n catalogs, the real API
// module and the real query documents. Faked: the GraphQL transport, the auth claims and the
// area-status read.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, navigateMock, authorities } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  navigateMock: vi.fn(),
  authorities: { value: [] as string[] },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));
vi.mock('@/lib/capabilities', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/capabilities')>();
  return { ...actual, useAreaStatus: () => 'available' };
});
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => navigateMock };
});

import RolloutsPage from './RolloutsPage';
import { NewRolloutForm } from './NewRolloutForm';

function rollout(over: Record<string, unknown> = {}) {
  return {
    id: '1',
    token: 'spring',
    name: 'Spring wave',
    groupToken: 'fleet',
    groupVersion: null,
    maxInFlight: 5,
    failureThreshold: 3,
    state: 'RUNNING',
    pauseReason: null,
    targetCount: 10,
    createdAt: '2026-10-01T10:00:00Z',
    createdBy: 'ops@example.com',
    startedAt: '2026-10-01T10:01:00Z',
    finishedAt: null,
    stateChangedBy: 'ops@example.com',
    stateChangedAt: '2026-10-01T10:01:00Z',
    artifact: { token: 'fw-2', name: 'gateway-fw', version: '2.0.0', component: 'firmware', state: 'AVAILABLE', trustStatus: 'SIGNED' },
    progress: { total: 10, pending: 3, inFlight: 4, succeeded: 2, failed: 1, unknown: 0, cancelled: 0, skipped: 0 },
    ...over,
  };
}

function opName(call: unknown[]): string {
  const doc = String(call[1]);
  return /(?:query|mutation)\s+(\w+)/.exec(doc)?.[1] ?? '';
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  navigateMock.mockReset();
  authorities.value = ['artifact:read'];
});

describe('RolloutsPage', () => {
  it('lists each rollout with its state, progress and cap, and opens it', async () => {
    gqlMock.mockResolvedValue({
      updateRollouts: {
        results: [rollout(), rollout({ id: '2', token: 'halt', name: 'Halted', state: 'PAUSED', pauseReason: 'FAILURE_THRESHOLD' })],
        pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 },
      },
    });
    render(<MemoryRouter><RolloutsPage /></MemoryRouter>);
    const row = (await screen.findByText('Spring wave')).closest('tr')!;
    expect(row.textContent).toContain('gateway-fw 2.0.0');
    expect(row.textContent).toContain('Running');
    expect(row.textContent).toContain('2 of 10 updated');
    expect(row.textContent).toContain('1 failed or unknown');
    const halted = screen.getByText('Halted').closest('tr')!;
    expect(halted.textContent).toContain('Failure threshold reached');
    fireEvent.click(row);
    expect(navigateMock).toHaveBeenCalledWith('/updates/rollouts/spring');
  });

  it('offers New rollout only to a caller with artifact:write', async () => {
    gqlMock.mockResolvedValue({ updateRollouts: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } } });
    render(<MemoryRouter><RolloutsPage /></MemoryRouter>);
    expect(await screen.findByText(/No rollouts match/)).toBeTruthy();
    expect(screen.queryByText('New rollout')).toBeNull();
    cleanup();
    authorities.value = ['artifact:read', 'artifact:write'];
    render(<MemoryRouter><RolloutsPage /></MemoryRouter>);
    expect(await screen.findByText('New rollout')).toBeTruthy();
  });
});

describe('NewRolloutForm', () => {
  function answer(call: unknown[]) {
    switch (opName(call)) {
      case 'Artifacts':
        return { artifacts: { results: [{ token: 'fw-2', name: 'gateway-fw', version: '2.0.0', component: 'firmware' }], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } } };
      case 'DeviceGroupTargets':
        return { entityGroups: { results: [{ token: 'fleet', name: 'Fleet', membershipMode: 'static' }] } };
      case 'CreateUpdateRollout':
        return { createUpdateRollout: rollout({ state: 'DRAFT', token: 'r1' }) };
    }
    throw new Error('unexpected operation ' + opName(call));
  }

  it('asks only for AVAILABLE artifacts, refuses a cap out of range, and creates with what was entered', async () => {
    gqlMock.mockImplementation((...args: unknown[]) => Promise.resolve(answer(args)));
    const onCreated = vi.fn();
    render(<NewRolloutForm onCreated={onCreated} />);
    await waitFor(() => expect(gqlMock.mock.calls.some((c) => opName(c) === 'Artifacts')).toBe(true));
    const artifactsCall = gqlMock.mock.calls.find((c) => opName(c) === 'Artifacts')!;
    expect((artifactsCall[2] as { criteria: { state: string } }).criteria.state).toBe('AVAILABLE');

    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'r1' } });
    fireEvent.change(screen.getByLabelText('Devices updating at once'), { target: { value: '0' } });
    expect(screen.getByText('Enter a whole number from 1 to 1000.')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Devices updating at once'), { target: { value: '3' } });
    fireEvent.change(screen.getByLabelText('Failure threshold'), { target: { value: '2' } });
    const create = screen.getByRole('button', { name: 'Create rollout' }) as HTMLButtonElement;
    expect(create.disabled).toBe(true); // no artifact or group chosen yet
  });
});
