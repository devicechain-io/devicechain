// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn(), authorities: [] as string[] }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: h.authorities } }) }));
import { MaintenanceBanner } from './EntityMaintenance';

const until = new Date(Date.now() + 3 * 3600000).toISOString();
const broad = {
  token: 'weekly', name: 'Weekly', description: null, reason: 'Patch night', targets: [{ type: 'AREA', token: 'plant' }],
  alarmKeys: [], bypassSeverity: null, kind: 'ONE_SHOT', startTime: new Date(Date.now() - 3600000).toISOString(), endTime: until,
  timezone: null, slots: null, validFrom: null, validUntil: null, status: 'ACTIVE', endedAt: null, updatedAt: null,
  openings: [],
};
beforeEach(() => {
  h.gql.mockReset().mockImplementation((_service, doc) => {
    if (String(doc).includes('query AlarmMaintenanceForDevice')) return Promise.resolve({ alarmSuppressionsForDevice: [broad] });
    return Promise.resolve({ endAlarmSuppressionWindow: { token: 'weekly' }, updateAlarmSuppressionWindow: { token: 'weekly' } });
  });
});
afterEach(cleanup);
const show = () => render(<MaintenanceBanner target={{ type: 'DEVICE', token: 'pump' }} />);
const enabled = async (name: string) => {
  const button = await screen.findByRole('button', { name }) as HTMLButtonElement;
  await waitFor(() => expect(h.gql).toHaveBeenCalled());
  return button;
};
describe('maintenance banner on a broad window', () => {
  it('lets alarm:write alone end it, but not extend it', async () => {
    h.authorities = ['alarm:read', 'alarm:write'];
    show();
    const end = await enabled('End now');
    await waitFor(() => expect(end.disabled).toBe(false));
    expect((screen.getByRole('button', { name: 'Extend +1 h' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(end);
    await waitFor(() => expect(h.gql.mock.calls.some(call => String(call[1]).includes('mutation AlarmMaintenanceEnd') && call[2].token === 'weekly')).toBe(true));
  });
  it('lets notification:write extend it too', async () => {
    h.authorities = ['alarm:read', 'alarm:write', 'notification:write'];
    show();
    const extend = await enabled('Extend +1 h') as HTMLButtonElement;
    await waitFor(() => expect(extend.disabled).toBe(false));
    fireEvent.click(extend);
    await waitFor(() => expect(h.gql.mock.calls.some(call => String(call[1]).includes('mutation AlarmMaintenanceUpdate') && call[2].token === 'weekly')).toBe(true));
  });
  it('refuses End without alarm:write', async () => {
    h.authorities = ['alarm:read', 'notification:write'];
    show();
    const end = await enabled('End now');
    expect(end.disabled).toBe(true);
  });
});
