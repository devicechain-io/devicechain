// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The policy form's quiet-hours behaviour, run inside the real form: the toggle, what a create
// sends, what turning quiet hours off sends (two nulls in one request, bypass untouched), that
// `rules` is never named, and that a schedule the editor cannot read is never opened as empty.
// The transport is the only fake.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: true, isIdentityAuthenticated: false, claims: { authorities: ['notification:write'] } }),
}));
vi.mock('@/lib/token-masks', () => ({ getTokenMasks: () => Promise.resolve({}) }));

import { NotificationPolicyForm } from './NotificationPolicyForm';
import type { NotificationPolicyRecord } from '@/lib/api/notifications';

const SLOTS = '[{"day":"MONDAY","start":"22:00","durationMinutes":480}]';

function stored(over: Partial<NotificationPolicyRecord> = {}): NotificationPolicyRecord {
  return {
    id: 'p1',
    token: 'night',
    name: 'Night',
    description: null,
    enabled: true,
    throttleSeconds: null,
    escalateAfterSeconds: null,
    maxEscalations: null,
    metadata: null,
    quietHoursTimezone: 'Europe/Berlin',
    quietHoursSlots: SLOTS,
    quietHoursBypassSeverity: 'MAJOR',
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-02T00:00:00Z',
    rules: [
      { id: 'r1', severity: 'CRITICAL', recipients: '["a@example.com"]', channel: { token: 'mail', name: 'Mail' } },
    ],
    ...over,
  };
}

type Write = { token?: unknown; request: Record<string, unknown>; expectedUpdatedAt?: unknown };
function writes(): Write[] {
  const out: Write[] = [];
  for (const call of gqlMock.mock.calls) {
    const vars = call[2] as Write | undefined;
    if (vars?.request == null) continue;
    out.push(vars);
  }
  return out;
}

const quietToggle = () => screen.getByRole('checkbox', { name: 'Hold notifications during quiet hours' });

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockResolvedValue({
    createNotificationPolicy: { token: 'x', updatedAt: 't' },
    updateNotificationPolicy: { token: 'x', updatedAt: 't' },
  });
});

describe('NotificationPolicyForm quiet hours', () => {
  it('renders the section off for a policy without quiet hours, and on for one with them', () => {
    const { unmount } = render(
      <NotificationPolicyForm
        entity={stored({ quietHoursTimezone: null, quietHoursSlots: null, quietHoursBypassSeverity: null })}
        onDone={vi.fn()}
      />,
    );
    expect(quietToggle().getAttribute('aria-checked')).toBe('false');
    expect(screen.queryByTestId('slot-editor')).toBeNull();
    unmount();

    render(<NotificationPolicyForm entity={stored()} onDone={vi.fn()} />);
    expect(quietToggle().getAttribute('aria-checked')).toBe('true');
    expect(screen.getAllByTestId('slot-row')).toHaveLength(1);
  });

  it('shows the rules read-only', () => {
    render(<NotificationPolicyForm entity={stored()} onDone={vi.fn()} />);
    const rule = screen.getByTestId('policy-rule');
    expect(rule.textContent).toContain('Critical');
    expect(rule.textContent).toContain('mail');
    expect(rule.textContent).toContain('a@example.com');
  });

  it('turning quiet hours off sends both nulls in one request, passes expectedUpdatedAt, never names rules', async () => {
    render(<NotificationPolicyForm entity={stored()} onDone={vi.fn()} />);
    fireEvent.click(quietToggle());
    expect(screen.queryByTestId('slot-editor')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    const w = writes()[0];
    expect(w.token).toBe('night');
    expect(w.request).toEqual({ quietHoursTimezone: null, quietHoursSlots: null });
    expect('rules' in w.request).toBe(false);
    expect('quietHoursBypassSeverity' in w.request).toBe(false);
    expect(w.expectedUpdatedAt).toBe('2026-08-02T00:00:00Z');
  });

  it('turning quiet hours on proposes CRITICAL as the bypass and sends the whole schedule', async () => {
    render(
      <NotificationPolicyForm
        entity={stored({ quietHoursTimezone: null, quietHoursSlots: null, quietHoursBypassSeverity: null })}
        onDone={vi.fn()}
      />,
    );
    fireEvent.click(quietToggle());
    expect(screen.getAllByTestId('slot-row')).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    const { request } = writes()[0];
    expect(typeof request.quietHoursTimezone).toBe('string');
    expect(request.quietHoursSlots).toBe(SLOTS);
    expect(request.quietHoursBypassSeverity).toBe('CRITICAL');
  });

  it('creates a policy with no rules', async () => {
    render(<NotificationPolicyForm onDone={vi.fn()} />);
    fireEvent.change(document.getElementById('np-token')!, { target: { value: 'quiet' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create notification policy' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    const { request } = writes()[0];
    expect(request.token).toBe('quiet');
    expect(request.rules).toEqual([]);
    expect(request.enabled).toBe(true);
    expect('quietHoursSlots' in request).toBe(false);
  });

  it('blocks a save while two slots overlap, and says why', () => {
    // Saturday 02:00 for two hours; the added row is also Saturday (the editor's default day).
    render(
      <NotificationPolicyForm
        entity={stored({ quietHoursSlots: '[{"day":"SATURDAY","start":"02:00","durationMinutes":120}]' })}
        onDone={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Add slot' }));
    const rows = screen.getAllByTestId('slot-row');
    fireEvent.change(within(rows[1]).getByLabelText('Start time'), { target: { value: '03:00' } });
    fireEvent.change(within(rows[1]).getByLabelText('Duration in minutes'), { target: { value: '60' } });
    expect(screen.getByTestId('quiet-coverage-problem').textContent).toMatch(/overlap/);
    expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('blocks a save that covers the whole week', () => {
    render(<NotificationPolicyForm entity={stored()} onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Duration in minutes'), { target: { value: '10080' } });
    expect(screen.getByTestId('quiet-coverage-problem').textContent).toMatch(/whole week/);
    expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('does not open an unreadable schedule as empty, and does not write over it', async () => {
    render(<NotificationPolicyForm entity={stored({ quietHoursSlots: '{"weird":true}' })} onDone={vi.fn()} />);
    expect(screen.queryByTestId('slot-editor')).toBeNull();
    expect(screen.getByText(/cannot read/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Renamed' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].request).toEqual({ name: 'Renamed' });
  });
});
