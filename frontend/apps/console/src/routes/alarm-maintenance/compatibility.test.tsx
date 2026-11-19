// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn() }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['*'], scopeAnchors: [] } }) }));
vi.mock('@/lib/hooks/use-alarm-stream', () => ({ useAlarmStream: () => ({ status: 'live' }) }));
import { GraphQLRequestError } from '@devicechain/client';
import { DeviceAlarmsPanel } from '@/routes/devices/DeviceAlarmsPanel';
import AlarmsPage from '@/routes/alarms/AlarmsPage';
import { ToastProvider } from '@/components/ui/toast';
import { StartMaintenanceAction, MaintenanceBanner } from './EntityMaintenance';

const alarm = { id: 'a', token: 'a', originatorType: 'device', originatorToken: 'pump', alarmKey: 'overheat', metricKey: 'temperature', state: 'ACTIVE', severity: 'MAJOR', acknowledged: false, raisedTime: '2026-01-01T00:00:00Z' };
beforeEach(() => {
  h.gql.mockReset().mockImplementation((_service, doc) => {
    if (String(doc).includes('query AlarmMaintenance')) {
      const message = String(doc).includes('query AlarmMaintenanceHistoryMarks')
        ? 'Cannot query field "raisedDuringSuppression" on type "Alarm".'
        : 'Cannot query field "alarmSuppressionStatus" on type "Query".';
      return Promise.reject(new GraphQLRequestError(message, 200, [{ message }]));
    }
    return Promise.resolve({ alarms: { results: [alarm], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } } });
  });
});
afterEach(cleanup);
describe('optional maintenance reads on an old server', () => {
  it.each(['list', 'device'])('keeps the %s alarms visible without a chip', async view => {
    render(<MemoryRouter><ToastProvider>{view === 'list' ? <AlarmsPage /> : <DeviceAlarmsPanel deviceToken="pump" />}</ToastProvider></MemoryRouter>);
    expect(await screen.findByText('overheat')).toBeTruthy();
    await waitFor(() => expect(h.gql.mock.calls.some(call => String(call[1]).includes('query AlarmMaintenanceStatus'))).toBe(true));
    await waitFor(() => expect(h.gql.mock.calls.some(call => String(call[1]).includes('query AlarmMaintenanceHistoryMarks'))).toBe(true));
    expect(screen.queryByText('Raised during maintenance')).toBeNull();
    expect(screen.queryByText(/Maintenance.*until/)).toBeNull();
    expect(screen.queryByRole('combobox', { name: /Raised during maintenance/ })).toBeNull();
    expect(screen.queryByText(/Failed to load/)).toBeNull();
  });
  it('hides both the device header action and active banner', async () => {
    render(<><StartMaintenanceAction target={{ type: 'DEVICE', token: 'pump' }} /><MaintenanceBanner target={{ type: 'DEVICE', token: 'pump' }} /></>);
    await waitFor(() => expect(h.gql).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('button', { name: 'Start maintenance' })).toBeNull();
    expect(screen.queryByText(/Under maintenance/)).toBeNull();
  });
});
