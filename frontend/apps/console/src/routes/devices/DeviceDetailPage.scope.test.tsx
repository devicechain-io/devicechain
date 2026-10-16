// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the device page offers a member limited to part of the tenant (a customer user).
// The server serves them the device, its live state, events, commands and updates, and
// refuses its assignment, facet values, stored configuration and every change to the
// device itself — so those tabs and actions are left out rather than shown to fail.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, claims } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
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
  return { ...actual, useNavigate: () => vi.fn(), useParams: () => ({ token: 'dev-1' }) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: claims.value }) }));
vi.mock('@/lib/updateAuthority', () => ({
  useUpdateAccess: () => ({ status: 'available', present: true, canRead: true, canWrite: false, canTrust: false }),
}));

import DeviceDetailPage from './DeviceDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string) =>
    Promise.resolve(
      service === 'device-management'
        ? {
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
          }
        : {},
    ),
  );
});

function renderPage(scope: string[]) {
  claims.value = { ...claims.value, scopeAnchors: scope };
  render(
    <MemoryRouter>
      <ConfirmProvider>
        <DeviceDetailPage />
      </ConfirmProvider>
    </MemoryRouter>,
  );
}

const SERVED = ['Connectivity', 'Events', 'Commands', 'Updates'];
const REFUSED = ['Assignment', 'Facets', 'Configuration', 'Credentials', 'Send data', 'Replacement'];

describe('the device page for a customer user', () => {
  it('shows only the tabs the server serves a scoped member, and no device actions', async () => {
    renderPage(['customer:acme']);
    for (const name of SERVED) expect(await screen.findByRole('tab', { name }), name).toBeTruthy();
    for (const name of REFUSED) expect(screen.queryByRole('tab', { name }), name).toBeNull();
    expect(screen.queryByRole('button', { name: /Delete/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Disable/ })).toBeNull();
  });

  // The negative control: the same authorities without a scope get every tab, so the cut
  // above is the scope's doing and not the fixture's.
  it('shows every tab to the same authorities without a scope', async () => {
    renderPage([]);
    for (const name of [...SERVED, ...REFUSED]) expect(await screen.findByRole('tab', { name }), name).toBeTruthy();
    expect(screen.getByRole('button', { name: /Delete/ })).toBeTruthy();
  });
});
