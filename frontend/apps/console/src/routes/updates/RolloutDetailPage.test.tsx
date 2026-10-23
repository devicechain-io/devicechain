// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The rollout detail page under jsdom with the real i18n catalogs, API module, query documents
// and confirm dialog. Faked: the GraphQL transport, the auth claims and the area-status read.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, authorities } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
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

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import RolloutDetailPage from './RolloutDetailPage';

function rollout(over: Record<string, unknown> = {}) {
  return {
    id: '1',
    token: 'spring',
    name: 'Spring wave',
    groupToken: 'fleet',
    groupVersion: 4,
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

const world = {
  rollout: rollout(),
  calls: [] as Array<{ op: string; vars: Record<string, unknown> }>,
};

function opName(doc: string): string {
  return /(?:query|mutation)\s+(\w+)/.exec(doc)?.[1] ?? '';
}

afterEach(cleanup);
beforeEach(() => {
  authorities.value = ['artifact:read', 'artifact:write'];
  world.rollout = rollout();
  world.calls = [];
  gqlMock.mockReset();
  gqlMock.mockImplementation((_area: string, doc: unknown, vars: Record<string, unknown>) => {
    const op = opName(String(doc));
    world.calls.push({ op, vars });
    switch (op) {
      case 'UpdateRollout':
        return Promise.resolve({ updateRollout: world.rollout });
      case 'UpdateRolloutTargets':
        return Promise.resolve({
          updateRolloutTargets: {
            results: [
              { deviceToken: 'dev-1', outcome: 'SUCCEEDED', assignmentToken: 'asg-1', attemptState: 'UPDATED', skipCode: null, skipDetail: null, dispatchedAt: '2026-10-01T10:01:00Z' },
              { deviceToken: 'dev-2', outcome: 'SKIPPED', assignmentToken: null, attemptState: null, skipCode: 'DEVICE_DISABLED', skipDetail: 'device "dev-2" is disabled', dispatchedAt: null },
            ],
            pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 },
          },
        });
      case 'PauseUpdateRollout':
        world.rollout = rollout({ state: 'PAUSED', pauseReason: 'OPERATOR' });
        return Promise.resolve({ pauseUpdateRollout: world.rollout });
      case 'ResumeUpdateRollout':
        world.rollout = rollout({ state: 'RUNNING' });
        return Promise.resolve({ resumeUpdateRollout: world.rollout });
      case 'AbortUpdateRollout':
        world.rollout = rollout({ state: 'ABORTED' });
        return Promise.resolve({ abortUpdateRollout: world.rollout });
      case 'StartUpdateRollout':
        world.rollout = rollout({ state: 'RUNNING' });
        return Promise.resolve({ startUpdateRollout: world.rollout });
    }
    return Promise.reject(new Error('unexpected operation ' + op));
  });
});

function renderPage() {
  render(
    <ConfirmProvider>
      <MemoryRouter initialEntries={['/updates/rollouts/spring']}>
        <Routes>
          <Route path="/updates/rollouts/:token" element={<RolloutDetailPage />} />
        </Routes>
      </MemoryRouter>
    </ConfirmProvider>,
  );
}

const sent = (op: string) => world.calls.filter((c) => c.op === op);

describe('RolloutDetailPage', () => {
  it('shows the facts, the counts and each device with its outcome or refusal', async () => {
    renderPage();
    expect(await screen.findByText('Spring wave')).toBeTruthy();
    expect(screen.getByText('fleet (version 4)')).toBeTruthy();
    const counts = screen.getByTestId('rollout-counts');
    expect(counts.textContent).toContain('In flight4');
    expect(counts.textContent).toContain('Succeeded2');
    const skipped = (await screen.findByText('dev-2')).closest('tr')!;
    expect(skipped.textContent).toContain('Skipped');
    expect(skipped.textContent).toContain('DEVICE_DISABLED');
    expect(screen.getByText('dev-1').closest('tr')!.textContent).toContain('Updated');
  });

  it('pauses a running rollout, and offers no Start or Resume while it runs', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Pause/ }));
    await waitFor(() => expect(sent('PauseUpdateRollout')).toHaveLength(1));
    expect(sent('PauseUpdateRollout')[0].vars).toEqual({ token: 'spring' });
    expect(await screen.findByRole('button', { name: /Resume/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Pause/ })).toBeNull();
  });

  it('aborts only after a confirmation that says in-flight devices are left to finish', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Abort/ }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog.textContent).toContain('The 4 device(s) updating when this page was loaded are left to finish');
    expect(sent('AbortUpdateRollout')).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abort' }));
    await waitFor(() => expect(sent('AbortUpdateRollout')).toHaveLength(1));
    // An aborted rollout has no controls left.
    await waitFor(() => expect(screen.queryByRole('button', { name: /Abort/ })).toBeNull());
  });

  it('does not offer a plain Resume after a threshold pause: it asks for a higher threshold', async () => {
    world.rollout = rollout({ state: 'PAUSED', pauseReason: 'FAILURE_THRESHOLD',
      progress: { total: 10, pending: 5, inFlight: 0, succeeded: 2, failed: 2, unknown: 1, cancelled: 0, skipped: 0 } });
    renderPage();
    expect(await screen.findByText('Paused at its failure threshold')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^Resume$/ })).toBeNull();
    const input = screen.getByLabelText('Failure threshold') as HTMLInputElement;
    expect(input.value).toBe('4'); // failed + unknown + 1
    fireEvent.change(input, { target: { value: '3' } });
    expect(screen.getByText('The new threshold must be more than 3.')).toBeTruthy();
    expect((screen.getByRole('button', { name: /Resume with this threshold/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(input, { target: { value: '6' } });
    fireEvent.click(screen.getByRole('button', { name: /Resume with this threshold/ }));
    await waitFor(() => expect(sent('ResumeUpdateRollout')).toHaveLength(1));
    expect(sent('ResumeUpdateRollout')[0].vars).toEqual({ token: 'spring', failureThreshold: 6 });
  });

  it('starts a draft after confirmation and shows no device table before the start', async () => {
    world.rollout = rollout({ state: 'DRAFT', startedAt: null, groupVersion: null,
      progress: { total: 0, pending: 0, inFlight: 0, succeeded: 0, failed: 0, unknown: 0, cancelled: 0, skipped: 0 } });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Start/ }));
    expect(sent('UpdateRolloutTargets')).toHaveLength(0);
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Start' }));
    await waitFor(() => expect(sent('StartUpdateRollout')).toHaveLength(1));
  });

  it('gives a reader no controls', async () => {
    authorities.value = ['artifact:read'];
    renderPage();
    expect(await screen.findByText('Spring wave')).toBeTruthy();
    for (const name of [/Pause/, /Abort/, /Start/, /Resume/]) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
  });

  it('says so when the tenant has no such rollout', async () => {
    world.rollout = null as unknown as ReturnType<typeof rollout>;
    renderPage();
    expect(await screen.findByText('No rollout with token spring.')).toBeTruthy();
  });
});
