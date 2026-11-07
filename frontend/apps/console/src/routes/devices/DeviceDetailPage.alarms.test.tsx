// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Alarms tab on the device page: offered to a member who may read alarms, left out for one
// who may not (rather than shown to fail), and it lists this device's alarms when opened.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, claims, route } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  route: { token: 'dev-1' },
  claims: {
    value: { tenant: 'oem', username: 'ann', roles: [], authorities: ['*'], typ: 'access', scopeAnchors: [] as string[] },
  },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => vi.fn(), useParams: () => ({ token: route.token }) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: claims.value }) }));
vi.mock('@/lib/updateAuthority', () => ({
  useUpdateAccess: () => ({ status: 'available', present: true, canRead: false, canWrite: false, canTrust: false }),
}));

import DeviceDetailPage from './DeviceDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

afterEach(cleanup);
beforeEach(() => {
  route.token = 'dev-1';
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, doc: unknown) => {
    if (service !== 'device-management') return Promise.resolve({});
    if (/query\s+Alarms\b/.test(String(doc))) {
      return Promise.resolve({ alarms: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } } });
    }
    return Promise.resolve({
      devicesByToken: [
        {
          id: 'id-1',
          token: 'dev-1',
          name: 'dev-1',
          description: '',
          externalId: null,
          metadata: null,
          createdAt: '2026-08-12T12:00:00Z',
          enabled: true,
          disabledAt: null,
          deviceType: { id: 'dt-1', token: 'sensor', name: 'Sensor', icon: 'cpu', backgroundColor: '#000', foregroundColor: '#fff', borderColor: '#000' },
        },
      ],
      deviceTypes: { results: [], pagination: {} },
    });
  });
});

function renderPage(authorities: string[]) {
  claims.value = { ...claims.value, authorities };
  render(
    <MemoryRouter>
      <ToastProvider>
        <ConfirmProvider>
          <DeviceDetailPage />
        </ConfirmProvider>
      </ToastProvider>
    </MemoryRouter>,
  );
}

describe('the Alarms tab on the device page', () => {
  it('is offered to a member who may read alarms, and lists this device alarms when opened', async () => {
    renderPage(['device:read', 'alarm:read']);
    const tab = await screen.findByRole('tab', { name: 'Alarms' });
    fireEvent.mouseDown(tab);
    fireEvent.click(tab);
    await waitFor(() =>
      expect(
        gqlMock.mock.calls.some(
          (c) =>
            /query\s+Alarms\b/.test(String(c[1])) &&
            c[2].criteria.originatorType === 'device' &&
            c[2].criteria.originatorToken === 'dev-1',
        ),
      ).toBe(true),
    );
  });

  it('is left out for a member who may not read alarms', async () => {
    renderPage(['device:read']);
    await screen.findByRole('tab', { name: 'Events' });
    expect(screen.queryByRole('tab', { name: 'Alarms' })).toBeNull();
  });
});

describe('moving between devices', () => {
  it('does not leave the previous device alarms standing under the next device', async () => {
    const row = {
      id: '1', token: 'alarm-1', originatorType: 'device', originatorId: '1', originatorToken: 'dev-1',
      alarmKey: 'dev-1-only-alarm', metricKey: 'temperature', state: 'ACTIVE', acknowledged: false,
      severity: 'MAJOR', raisedTime: '2026-10-01T00:00:00Z', clearedTime: null, acknowledgedTime: null,
      acknowledgedBy: null, lastValue: 1,
    };
    const base = gqlMock.getMockImplementation()!;
    gqlMock.mockImplementation((service: string, doc: unknown, vars: any) => {
      if (/query\s+Alarms\b/.test(String(doc))) {
        // The second device's alarms never arrive during this test.
        return vars.criteria.originatorToken === 'dev-1'
          ? Promise.resolve({ alarms: { results: [row], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } } })
          : new Promise(() => {});
      }
      return base(service, doc, vars);
    });
    claims.value = { ...claims.value, authorities: ['*'] };
    const view = () => (
      <MemoryRouter>
        <ToastProvider>
          <ConfirmProvider>
            <DeviceDetailPage />
          </ConfirmProvider>
        </ToastProvider>
      </MemoryRouter>
    );
    const { rerender } = render(view());
    const tab = await screen.findByRole('tab', { name: 'Alarms' });
    fireEvent.mouseDown(tab);
    fireEvent.click(tab);
    await screen.findByText('dev-1-only-alarm');

    route.token = 'dev-2';
    rerender(view());
    await waitFor(() => expect(screen.queryByText('dev-1-only-alarm')).toBeNull());
  });
});
