// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The discarded-keys notice on the device page: a device that sends a measurement key the
// platform cannot store (the text value of an undeclared key, say) gets a notice above the tabs
// and a table on the Connectivity tab, so the person who owns the device can find out which
// key is being lost without reading a pod's log.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, claims, params } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  params: { token: 'dev-1' },
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
  return { ...actual, useNavigate: () => vi.fn(), useParams: () => ({ token: params.token }) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: claims.value }) }));
vi.mock('@/lib/updateAuthority', () => ({
  useUpdateAccess: () => ({ status: 'available', present: true, canRead: true, canWrite: false, canTrust: false }),
}));

import DeviceDetailPage from './DeviceDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

const DEVICE = {
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
};

const DROPPED = [
  { id: '1', name: 'status_text', reason: 'UNDECLARED_NON_NUMERIC', lastValue: 'ok', count: 7, lastTime: '2026-11-01T12:00:00Z' },
  { id: '2', name: 'blob', reason: 'UNDECLARED_UNSTORABLE', lastValue: '1e30', count: 1, lastTime: '2026-11-01T11:00:00Z' },
];

// deviceState answers the device-state documents: the dropped record is `dropped`, or the
// whole service is refused when `dropped` is an Error (a role without state:read).
function serve(dropped: unknown) {
  gqlMock.mockImplementation((service: string, doc: unknown) => {
    if (service === 'device-management') {
      return Promise.resolve({ devicesByToken: [DEVICE], deviceTypes: { results: [], pagination: {} } });
    }
    if (service === 'device-state' && /query DroppedMeasurements/.test(String(doc))) {
      return dropped instanceof Error ? Promise.reject(dropped) : Promise.resolve({ droppedMeasurements: dropped });
    }
    return Promise.resolve({});
  });
}

function renderPage() {
  render(
    <MemoryRouter>
      <ConfirmProvider>
        <DeviceDetailPage />
      </ConfirmProvider>
    </MemoryRouter>,
  );
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  params.token = 'dev-1';
  claims.value = { ...claims.value, scopeAnchors: [], authorities: ['*'] };
});

describe('the discarded-keys notice on the device page', () => {
  it('says how many keys are discarded, from any tab, and links to the table', async () => {
    serve(DROPPED);
    renderPage();
    const notice = await screen.findByTestId('dropped-measurements-notice');
    expect(notice.textContent).toContain('2 metric keys from this device are being discarded');
    expect(notice.textContent).toContain('Declare a key as a numeric metric');
    // The table is on the Connectivity tab, which is not open yet.
    expect(screen.queryByTestId('dropped-measurements-table')).toBeNull();

    fireEvent.click(screen.getByRole('link', { name: 'Show the keys' }));
    const table = await screen.findByTestId('dropped-measurements-table');
    // Key, the reason in words, the last value, the count.
    expect(table.textContent).toContain('status_text');
    expect(table.textContent).toContain('Not declared on the profile, and the value is not a number');
    expect(table.textContent).toContain('ok');
    expect(table.textContent).toContain('7');
    expect(table.textContent).toContain('blob');
    expect(table.textContent).toContain('Not declared on the profile, and the number cannot be stored');
  });

  it('uses the singular for one key', async () => {
    serve([DROPPED[0]]);
    renderPage();
    const notice = await screen.findByTestId('dropped-measurements-notice');
    expect(notice.textContent).toContain('1 metric key from this device is being discarded');
  });

  // Negative controls: no notice, and no table, when there is nothing to say or it cannot be said.
  it('shows nothing for a device that loses nothing', async () => {
    serve([]);
    renderPage();
    await screen.findByRole('tab', { name: 'Connectivity' });
    await waitFor(() => expect(gqlMock.mock.calls.some((c) => /query DroppedMeasurements/.test(String(c[1])))).toBe(true));
    expect(screen.queryByTestId('dropped-measurements-notice')).toBeNull();
    fireEvent.mouseDown(screen.getByRole('tab', { name: 'Connectivity' }));
    await screen.findByText('Latest readings');
    expect(screen.queryByTestId('dropped-measurements-table')).toBeNull();
  });

  it('shows nothing, and does not break the page, when the read is refused', async () => {
    serve(new Error('forbidden: state:read required'));
    renderPage();
    await screen.findByRole('tab', { name: 'Connectivity' });
    await waitFor(() => expect(gqlMock.mock.calls.some((c) => /query DroppedMeasurements/.test(String(c[1])))).toBe(true));
    expect(screen.queryByTestId('dropped-measurements-notice')).toBeNull();
  });

  it('shows nothing when the service answers with something that is not a list', async () => {
    serve(undefined);
    renderPage();
    await screen.findByRole('tab', { name: 'Connectivity' });
    expect(screen.queryByTestId('dropped-measurements-notice')).toBeNull();
  });

  // The gateway and child links change only the token, so the page instance survives from device
  // A to device B. A's keys must not stand in for B's while B loads, nor after B's read fails.
  // (Negative control: reading the sticky `data` instead of `current` fails both assertions.)
  it("does not show device A's keys on device B while B loads or after B's read fails", async () => {
    const view = () => (
      <MemoryRouter>
        <ConfirmProvider>
          <DeviceDetailPage />
        </ConfirmProvider>
      </MemoryRouter>
    );
    let answerB: (v: unknown) => void = () => {};
    gqlMock.mockImplementation((service: string, doc: unknown) => {
      if (service === 'device-management') {
        return Promise.resolve({ devicesByToken: [{ ...DEVICE, token: params.token, name: params.token }], deviceTypes: { results: [], pagination: {} } });
      }
      if (service === 'device-state' && /query DroppedMeasurements/.test(String(doc))) {
        if (params.token === 'dev-1') return Promise.resolve({ droppedMeasurements: DROPPED });
        return new Promise((resolve, reject) => {
          answerB = (e) => (e instanceof Error ? reject(e) : resolve(e));
        });
      }
      return Promise.resolve({});
    });

    const { rerender } = render(view());
    await screen.findByTestId('dropped-measurements-notice');

    // Navigate to B; its dropped read is still in flight.
    params.token = 'dev-2';
    rerender(view());
    await waitFor(() => expect(gqlMock.mock.calls.some((c) => c[1] && JSON.stringify(c[2] ?? {}).includes('dev-2'))).toBe(true));
    expect(screen.queryByTestId('dropped-measurements-notice')).toBeNull();

    // B's read fails: still nothing, indefinitely.
    answerB(new Error('boom'));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByTestId('dropped-measurements-notice')).toBeNull();
  });
});
