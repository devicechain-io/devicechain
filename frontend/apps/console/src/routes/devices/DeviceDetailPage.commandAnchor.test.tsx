// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The approvals inbox links to `/devices/<token>?tab=commands#command-<token>`. The page reads the
// fragment and hands the command to the Commands tab, which scrolls to the row.
import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
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

const device = {
  id: 'id-dev-1', token: 'dev-1', name: 'Device One', description: '', externalId: null, metadata: null,
  createdAt: '2026-08-12T12:00:00Z', enabled: true, disabledAt: null,
  deviceType: { id: 'dt-1', token: 'sensor', name: 'Sensor', icon: 'cpu', backgroundColor: '#000', foregroundColor: '#fff', borderColor: '#000' },
};
const command = (token: string) => ({
  id: `id-${token}`, token, deviceToken: 'dev-1', batchToken: null, name: `cmd-${token}`, payload: null, status: 'SUCCEEDED',
  queuedTime: '2026-08-12T12:00:00Z', sentTime: null, respondedTime: null, expiresAt: null, dispatchAfter: null,
  maintenanceWindowToken: null, responsePayload: null, error: null, outcome: null,
});

const scrolled: string[] = [];
afterEach(() => {
  cleanup();
  delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
});
beforeEach(() => {
  scrolled.length = 0;
  Element.prototype.scrollIntoView = vi.fn(function (this: Element) { scrolled.push(this.id); });
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, document: unknown, vars?: { tokens?: string[] }) => {
    if (service === 'command-delivery') {
      if (String(document).includes('query CommandByToken')) return Promise.resolve({ commandsByToken: [command('old/9')] });
      return Promise.resolve({
        commands: { results: [command('c1')], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 30 } },
      });
    }
    if (service !== 'device-management') return Promise.resolve({});
    if (String(document).includes('deviceCommandVocabulary')) {
      return Promise.resolve({ deviceCommandVocabulary: { constrained: false, commands: [] } });
    }
    return Promise.resolve({
      devicesByToken: (vars?.tokens ?? []).map(() => device),
      deviceTypes: { results: [], pagination: {} },
      deviceCommandVocabulary: { constrained: false, commands: [] },
    });
  });
});

const open = (hash: string) =>
  render(
    <MemoryRouter initialEntries={[`/devices/dev-1?tab=commands${hash}`]}>
      <ConfirmProvider>
        <Routes>
          <Route path="/devices/:token" element={<DeviceDetailPage />} />
        </Routes>
      </ConfirmProvider>
    </MemoryRouter>,
  );

describe('the device page hands a command link to the Commands tab', () => {
  it('scrolls to a command on the first page', async () => {
    open('#command-c1');
    await screen.findByText('cmd-c1');
    await waitFor(() => expect(scrolled).toEqual(['command-c1']));
  });

  it('decodes the token and finds a command beyond the first page', async () => {
    open('#command-old%2F9');
    await screen.findByText('cmd-old/9');
    await waitFor(() => expect(scrolled).toEqual(['command-old/9']));
  });

  it('scrolls nowhere without a command fragment', async () => {
    open('');
    await screen.findByText('cmd-c1');
    expect(scrolled).toEqual([]);
  });
});
