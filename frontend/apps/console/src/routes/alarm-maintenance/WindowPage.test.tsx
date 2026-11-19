// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn() }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['*'] } }) }));
import WindowPage from './WindowPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
const stored = {
  token: 'service', name: 'Pump work', description: null, reason: 'Replace seal', targets: [{ type: 'DEVICE', token: 'pump' }],
  alarmKeys: [], bypassSeverity: null, kind: 'ONE_SHOT', startTime: '2099-01-01T00:00:00Z', endTime: '2099-01-01T01:00:00Z',
  timezone: null, slots: null, validFrom: null, validUntil: null, status: 'ACTIVE', endedAt: null, updatedAt: null,
  openings: [{ start: '2099-01-01T00:00:00Z', end: '2099-01-01T01:00:00Z' }],
};
beforeEach(() => { h.gql.mockReset().mockImplementation((_service, doc) => {
  if (String(doc).includes('query AlarmMaintenanceByToken')) return Promise.resolve({ alarmSuppressionWindowsByToken: [stored] });
  return Promise.resolve({ endAlarmSuppressionWindow: { token: 'service' }, deleteAlarmSuppressionWindow: true });
}); });
afterEach(cleanup);
function renderPage() {
  render(<MemoryRouter initialEntries={['/alarm-maintenance/service']}><ConfirmProvider><Routes><Route path="/alarm-maintenance/:token" element={<WindowPage />} /><Route path="/alarm-maintenance" element={<div data-testid="list" />} /></Routes></ConfirmProvider></MemoryRouter>);
}
describe('saved maintenance actions', () => {
  it('shows the saved server preview and ends using the end mutation', async () => {
    renderPage();
    expect(await screen.findByTestId('alarm-maintenance-openings')).toBeTruthy();
    const end = await screen.findByRole('button', { name: 'End now' });
    await waitFor(() => expect((end as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(end);
    await waitFor(() => expect(h.gql.mock.calls.some(call => String(call[1]).includes('mutation AlarmMaintenanceEnd') && call[2].token === 'service')).toBe(true));
  });
  it('confirms deletion before invoking the delete mutation', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Delete window' }));
    const dialog = await screen.findByRole('dialog');
    expect(h.gql.mock.calls.some(call => String(call[1]).includes('mutation AlarmMaintenanceDelete'))).toBe(false);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByTestId('list')).toBeTruthy();
    expect(h.gql.mock.calls.find(call => String(call[1]).includes('mutation AlarmMaintenanceDelete'))?.[2]).toEqual({ token: 'service' });
  });
});
