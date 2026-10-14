// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The slot editor, run inside the real form: what it sends to the server on a create (the
// canonical `slots` document) and on an edit (only what changed). The validation shown is a
// hint — the server decides — so this checks the hint does not block a good schedule and does
// block an obviously broken one from being sent. The transport is the only fake.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The token field reads the tenant's token masks through the auth context; neither matters here.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: true, isIdentityAuthenticated: false, claims: { authorities: ['device:write'] } }),
}));
vi.mock('@/lib/token-masks', () => ({ getTokenMasks: () => Promise.resolve({}) }));

import { MaintenanceWindowForm, windowEditRequest } from './MaintenanceWindowForm';
import type { MaintenanceWindow } from '@/lib/api/maintenance-windows';

const SLOTS = '[{"day":"SATURDAY","start":"02:00","durationMinutes":120}]';

function stored(over: Partial<MaintenanceWindow> = {}): MaintenanceWindow {
  return {
    id: 'w1',
    token: 'night',
    name: 'Night',
    description: null,
    timezone: 'Europe/Berlin',
    slots: SLOTS,
    metadata: null,
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
    ...over,
  };
}

type Write = { token?: unknown; request: Record<string, unknown> };
function writes(): Write[] {
  const out: Write[] = [];
  for (const call of gqlMock.mock.calls) {
    const vars = call[2] as { request?: Record<string, unknown>; token?: unknown } | undefined;
    if (vars?.request == null) continue;
    out.push({ token: vars.token, request: vars.request });
  }
  return out;
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation((_svc: string, doc: unknown) => {
    if (String(doc).includes('query MaintenanceWindowOpening')) {
      return Promise.resolve({
        maintenanceWindowOpening: {
          found: true,
          openNow: false,
          dispatchAfter: '2026-10-17T00:00:00Z',
          closes: '2026-10-17T02:00:00Z',
        },
      });
    }
    return Promise.resolve({ createMaintenanceWindow: { token: 'x' }, updateMaintenanceWindow: { token: 'x' } });
  });
});

describe('windowEditRequest', () => {
  const same = { name: 'Night', description: '', timezone: 'Europe/Berlin', slots: [{ day: 'SATURDAY' as const, start: '02:00', duration: '120' }] };

  it('is empty when nothing changed', () => {
    expect(windowEditRequest(stored(), same)).toEqual({});
  });

  it('names only what changed', () => {
    expect(windowEditRequest(stored(), { ...same, timezone: 'UTC' })).toEqual({ timezone: 'UTC' });
    expect(windowEditRequest(stored(), { ...same, name: '' })).toEqual({ name: null });
    expect(
      windowEditRequest(stored(), { ...same, slots: [{ day: 'SUNDAY', start: '03:15', duration: '90' }] }),
    ).toEqual({ slots: '[{"day":"SUNDAY","start":"03:15","durationMinutes":90}]' });
  });
});

describe('MaintenanceWindowForm', () => {
  it('creates a window with the canonical slots document', async () => {
    render(<MaintenanceWindowForm onDone={vi.fn()} />);
    fireEvent.change(document.getElementById('mw-token')!, { target: { value: 'night' } });
    // The default row is Saturday 02:00 for 120 minutes; add a second and edit it.
    fireEvent.click(screen.getByRole('button', { name: 'Add slot' }));
    const rows = screen.getAllByTestId('slot-row');
    expect(rows).toHaveLength(2);
    fireEvent.change(within(rows[1]).getByLabelText('Start time'), { target: { value: '23:30' } });
    fireEvent.change(within(rows[1]).getByLabelText('Duration in minutes'), { target: { value: '150' } });
    expect(within(rows[1]).getByTestId('slot-duration-readout').textContent).toBe('2 h 30 min');

    fireEvent.click(screen.getByRole('button', { name: 'Create maintenance window' }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    const { request } = writes()[0];
    expect(request.token).toBe('night');
    expect(typeof request.timezone).toBe('string');
    expect(request.slots).toBe(
      '[{"day":"SATURDAY","start":"02:00","durationMinutes":120},' +
        '{"day":"SATURDAY","start":"23:30","durationMinutes":150}]',
    );
  });

  it('does not send a schedule the hint can already see is broken', async () => {
    render(<MaintenanceWindowForm onDone={vi.fn()} />);
    fireEvent.change(document.getElementById('mw-token')!, { target: { value: 'night' } });
    const row = screen.getByTestId('slot-row');
    fireEvent.change(within(row).getByLabelText('Duration in minutes'), { target: { value: '99999' } });
    expect(screen.getByTestId('slot-problem')).toBeTruthy();
    const create = screen.getByRole('button', { name: 'Create maintenance window' }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
  });

  it('removes a row, and flags an empty list', () => {
    render(<MaintenanceWindowForm onDone={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Remove slot' }));
    expect(screen.queryAllByTestId('slot-row')).toHaveLength(0);
    expect(screen.getByTestId('slot-count-problem')).toBeTruthy();
  });

  it('on an edit sends only the field that changed, and shows the server answer for the opening', async () => {
    render(<MaintenanceWindowForm entity={stored()} onDone={vi.fn()} />);
    const opening = await screen.findByTestId('window-opening');
    await waitFor(() => expect(opening.textContent).toMatch(/Closed now\. It next opens/));

    const save = screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Night shift' } });
    fireEvent.click(save);
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].token).toBe('night');
    expect(writes()[0].request).toEqual({ name: 'Night shift' });
  });

  it('refuses to open a schedule it cannot read rather than showing an empty one', () => {
    render(<MaintenanceWindowForm entity={stored({ slots: '{"weird":true}' })} onDone={vi.fn()} />);
    expect(screen.queryByRole('button', { name: 'Save changes' })).toBeNull();
    expect(screen.getByText(/cannot read/)).toBeTruthy();
  });
});
