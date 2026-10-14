// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pins that the device page's Updates tab is wired to the update-management access check, and
// hands the panel the facts it needs. The panel itself is faked: its behaviour is covered in
// DeviceUpdatesPanel.test.tsx, and this file is about WHEN the tab exists.
//
// 🔴 The tab belongs to an opt-in area, so it must be absent unless the area is known to be
// deployed AND the caller may read it. A tab shown on an instance without the area would 404 on
// every open; one shown to a caller without artifact:read would 403.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, access } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  access: {
    value: { status: 'available', present: true, canRead: true, canWrite: true, canTrust: false } as {
      status: string;
      present: boolean;
      canRead: boolean;
      canWrite: boolean;
      canTrust: boolean;
    },
  },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => vi.fn(), useParams: () => ({ token: 'dev-1' }) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: { tenant: 'acme', username: 'op', roles: [], authorities: ['device:read'], typ: 'access' },
  }),
}));
vi.mock('@/lib/updateAuthority', () => ({ useUpdateAccess: () => access.value }));
vi.mock('./DeviceUpdatesPanel', () => ({
  DeviceUpdatesPanel: (p: { deviceToken: string; deviceEnabled: boolean; canWrite: boolean }) => (
    <div data-testid="updates-panel">{`${p.deviceToken}|${p.deviceEnabled}|${p.canWrite}`}</div>
  ),
}));

import DeviceDetailPage from './DeviceDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

afterEach(cleanup);
beforeEach(() => gqlMock.mockReset());

function renderPage(enabled = true) {
  gqlMock.mockImplementation((service: string) => {
    if (service === 'device-management') {
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
            enabled,
            disabledAt: enabled ? null : '2026-08-12T12:00:00Z',
            deviceType: {
              id: 'dt-1',
              token: 'sensor',
              name: 'Sensor',
              icon: 'cpu',
              backgroundColor: '#000000',
              foregroundColor: '#ffffff',
              borderColor: '#000000',
            },
          },
        ],
        deviceTypes: { results: [], pagination: {} },
      });
    }
    return Promise.resolve({});
  });
  render(
    <MemoryRouter>
      <ConfirmProvider>
        <DeviceDetailPage />
      </ConfirmProvider>
    </MemoryRouter>,
  );
}

const setAccess = (v: Partial<typeof access.value>) => {
  access.value = { status: 'available', present: true, canRead: true, canWrite: true, canTrust: false, ...v };
};

describe('the Updates tab', () => {
  it('is shown when the area is deployed and the caller may read it, and carries the device facts', async () => {
    setAccess({ canWrite: true });
    renderPage(false);
    fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Updates' }));
    expect((await screen.findByTestId('updates-panel')).textContent).toBe('dev-1|false|true');
  });

  it('passes read-only access through to the panel', async () => {
    setAccess({ canWrite: false });
    renderPage();
    fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Updates' }));
    expect((await screen.findByTestId('updates-panel')).textContent).toBe('dev-1|true|false');
  });

  it('is absent while the area is not known to be deployed', async () => {
    setAccess({ status: 'unknown', present: false, canRead: false, canWrite: false });
    renderPage();
    await screen.findByRole('tab', { name: 'Commands' });
    expect(screen.queryByRole('tab', { name: 'Updates' })).toBeNull();
  });

  it('is absent for a caller who cannot read artifacts', async () => {
    setAccess({ canRead: false, canWrite: false });
    renderPage();
    await screen.findByRole('tab', { name: 'Commands' });
    expect(screen.queryByRole('tab', { name: 'Updates' })).toBeNull();
  });
});
