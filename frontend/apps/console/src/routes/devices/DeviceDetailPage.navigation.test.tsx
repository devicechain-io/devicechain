// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Moving straight from one device to another on the same route. The device read was keyed
// on the reload counter alone, so the page kept showing — and every tab kept acting on —
// the FIRST device under the second device's URL.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: { tenant: 'oem', username: 'ann', roles: [], authorities: ['*'], typ: 'access', scopeAnchors: [] },
  }),
}));
vi.mock('@/lib/updateAuthority', () => ({
  useUpdateAccess: () => ({ status: 'available', present: false, canRead: false, canWrite: false, canTrust: false }),
}));

import DeviceDetailPage from './DeviceDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

function device(token: string, name: string) {
  return {
    id: `id-${token}`,
    token,
    name,
    description: '',
    externalId: null,
    metadata: null,
    createdAt: '2026-08-12T12:00:00Z',
    enabled: true,
    disabledAt: null,
    deviceType: { id: 'dt-1', token: 'sensor', name: 'Sensor', icon: 'cpu', backgroundColor: '#000', foregroundColor: '#fff', borderColor: '#000' },
  };
}

const DEVICES: Record<string, ReturnType<typeof device>> = {
  'dev-1': device('dev-1', 'Device One'),
  'dev-2': device('dev-2', 'Device Two'),
};

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, _doc: unknown, vars?: { tokens?: string[] }) => {
    if (service !== 'device-management') return Promise.resolve({});
    const tokens = vars?.tokens ?? [];
    return Promise.resolve({
      devicesByToken: tokens.map((t) => DEVICES[t]).filter(Boolean),
      deviceTypes: { results: [], pagination: {} },
    });
  });
});

function GoTo({ to }: { to: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(to)}>
      go-{to}
    </button>
  );
}

describe('DeviceDetailPage across a direct device-to-device navigation', () => {
  it('reads and shows the device the URL names', async () => {
    render(
      <MemoryRouter initialEntries={['/devices/dev-1']}>
        <ConfirmProvider>
          <GoTo to="/devices/dev-2" />
          <Routes>
            <Route path="/devices/:token" element={<DeviceDetailPage />} />
          </Routes>
        </ConfirmProvider>
      </MemoryRouter>,
    );
    expect(((await screen.findByLabelText('Name')) as HTMLInputElement).value).toBe('Device One');

    fireEvent.click(screen.getByRole('button', { name: 'go-/devices/dev-2' }));
    await waitFor(() =>
      expect(gqlMock).toHaveBeenCalledWith('device-management', expect.anything(), { tokens: ['dev-2'] }),
    );
    await waitFor(async () =>
      expect(((await screen.findByLabelText('Name')) as HTMLInputElement).value).toBe('Device Two'),
    );
  });
});
