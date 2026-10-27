// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The integration detail page: the traffic markers, the connect snippet (which can no longer show
// the bearer token), rotate (a fresh reveal-once), the unregistered-devices list with its
// create-a-device link and clear, and delete. Only the transport is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', tenant: 'acme', authorities: ['*'], scopeAnchors: [] as string[] },
  gql: vi.fn(),
  toast: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import IntegrationDetailPage from './IntegrationDetailPage';
import { rowFromParam } from '@/routes/devices/deviceAliases';

const ROTATED = 'Rotated-token-0123456789abcdefghijklmnopqrstu';

const INTEGRATION = {
  id: 'i-1',
  token: 'eu868-gw',
  name: 'EU868 gateway fleet',
  description: null,
  type: 'TTS_V3',
  enabled: true,
  identity: 'DEV_EUI',
  generic: null,
  linkMetrics: true,
  tokenIssuedAt: '2026-10-01T00:00:00Z',
  undecodedCount: 12,
  lastUndecodedAt: '2026-10-03T10:00:00Z',
  lastUndecodedSample: 'DEADBEEF01',
  lastAuthFailureAt: '2026-10-04T09:00:00Z',
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  deviceTypes: [{ id: 'dt-1', token: 'meter', name: 'Meter' }],
};

const UNREGISTERED = [
  { id: 'u-1', integration: 'eu868-gw', scheme: 'lorawan.deveui', value: '70B3D57ED0012345', firstSeen: '2026-10-03T10:00:00Z', lastSeen: '2026-10-04T10:00:00Z', count: 9 },
];

let sent: { op: string; text: string; variables: any }[] = [];
let unregistered = UNREGISTERED;

function install() {
  sent = [];
  unregistered = UNREGISTERED;
  h.gql.mockImplementation((service: string, doc: unknown, variables: any) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const text = String(doc);
    const op = /(?:query|mutation)\s+(\w+)/.exec(text)?.[1] ?? '';
    sent.push({ op, text, variables });
    switch (op) {
      case 'IntegrationByToken':
        return Promise.resolve({ integration: INTEGRATION });
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: { results: [{ id: 'dt-1', token: 'meter', name: 'Meter' }], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
        });
      case 'UnregisteredIntegrationDevices':
        return Promise.resolve({
          unregisteredIntegrationDevices: {
            results: unregistered,
            pagination: { pageStart: 1, pageEnd: unregistered.length, totalRecords: unregistered.length },
          },
        });
      case 'ClearUnregisteredIntegrationDevices': {
        const n = unregistered.length;
        unregistered = [];
        return Promise.resolve({ clearUnregisteredIntegrationDevices: n });
      }
      case 'RotateIntegrationToken':
        return Promise.resolve({ rotateIntegrationToken: { issuedToken: ROTATED, integration: INTEGRATION } });
      case 'DeleteIntegration':
        return Promise.resolve({ deleteIntegration: true });
      default:
        return Promise.resolve({});
    }
  });
}

function Where() {
  const l = useLocation();
  return <p data-testid="where">{l.pathname + l.search}</p>;
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/integrations/eu868-gw']}>
      <ConfirmProvider>
        <Routes>
          <Route path="/integrations/:token" element={<IntegrationDetailPage />} />
          <Route path="/integrations" element={<p>the list</p>} />
          <Route path="/devices" element={<Where />} />
        </Routes>
      </ConfirmProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', tenant: 'acme', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  install();
});
afterEach(cleanup);

describe('the traffic markers', () => {
  it('shows the undecoded counter, the last sample and the last auth failure from the integration', async () => {
    renderPage();
    await screen.findByText('Uplinks with nothing to read');
    expect(screen.getByText('12')).toBeTruthy();
    expect(screen.getByTestId('undecoded-sample').textContent).toBe('DEADBEEF01');
    expect(screen.getByText('Rejected token')).toBeTruthy();
    expect(screen.getByText('12 undecoded')).toBeTruthy();
  });
});

describe('the connect snippet', () => {
  it('uses the signed-in tenant and the integration token, and a placeholder where the bearer token was', async () => {
    renderPage();
    await screen.findByText('Connect the sender');
    const values = Array.from(document.querySelectorAll('input')).map((i) => i.value);
    expect(values).toContain('https://<ingest-host>/<instance-id>/acme/integrations/eu868-gw');
    expect(values).toContain('Authorization: Bearer <bearer-token>');
    expect(values).toContain('/uplink-message');
    // The Things Stack guidance: the paths read, and that others are ignored.
    expect(screen.getByText(/up, uplink, uplinks, uplink-message/)).toBeTruthy();
    expect(screen.getByText(/any other path are acknowledged and ignored/)).toBeTruthy();
  });
});

describe('rotating the token', () => {
  it('reveals the new token once and drops it on close, without ever reading it from a query', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate token' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate token', hidden: false }));
    await waitFor(() => expect(document.querySelector('[data-shot="integration-token"]')).not.toBeNull());
    expect((document.getElementById('ig-reveal-token') as HTMLInputElement).value).toBe(ROTATED);
    // The snippet inside the dialog carries the new token in the header.
    expect(Array.from(document.querySelectorAll('input')).map((i) => i.value)).toContain(`Authorization: Bearer ${ROTATED}`);

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(document.querySelector('[data-shot="integration-token"]')).toBeNull());
    expect(document.body.innerHTML).not.toContain(ROTATED);
    // The reload after the rotate never selects the token.
    await waitFor(() => expect(sent.filter((s) => s.op === 'IntegrationByToken').length).toBeGreaterThanOrEqual(2));
    expect(sent.filter((s) => s.text.includes('issuedToken')).map((s) => s.op)).toEqual(['RotateIntegrationToken']);
  });
});

describe('the unregistered devices', () => {
  it('lists what the integration saw, and links to a new device with the alias and the only type filled in', async () => {
    renderPage();
    await screen.findByText('70B3D57ED0012345');
    expect(screen.getByText('lorawan.deveui')).toBeTruthy();
    fireEvent.click(screen.getByRole('link', { name: 'Create a device with this alias' }));
    const where = await screen.findByTestId('where');
    const params = new URL(where.textContent!, 'http://x').searchParams;
    expect(rowFromParam(params.get('newAlias'))).toEqual({ kind: 'lorawan.deveui', customName: '', value: '70B3D57ED0012345' });
    expect(params.get('newType')).toBe('meter');
  });

  it('empties the list through clearUnregisteredIntegrationDevices after confirming', async () => {
    renderPage();
    await screen.findByText('70B3D57ED0012345');
    fireEvent.click(screen.getByRole('button', { name: 'Clear list' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Clear list', hidden: false }));
    await waitFor(() => expect(sent.some((s) => s.op === 'ClearUnregisteredIntegrationDevices')).toBe(true));
    expect(sent.find((s) => s.op === 'ClearUnregisteredIntegrationDevices')!.variables).toEqual({ integration: 'eu868-gw' });
    await screen.findByText(/None seen/);
    expect(screen.queryByText('70B3D57ED0012345')).toBeNull();
  });

  it('offers neither the link nor the clear to a member without device:write', async () => {
    h.claims = { username: 'reader@oem.example', tenant: 'acme', authorities: ['device:read'], scopeAnchors: [] };
    renderPage();
    await screen.findByText('70B3D57ED0012345');
    expect(screen.queryByRole('link', { name: 'Create a device with this alias' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Clear list' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Rotate token' })).toBeNull();
  });
});

describe('deleting', () => {
  it('deletes after confirming and returns to the list', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    const buttons = await screen.findAllByRole('button', { name: 'Delete' });
    fireEvent.click(buttons[buttons.length - 1]);
    await waitFor(() => expect(sent.some((s) => s.op === 'DeleteIntegration')).toBe(true));
    expect(await screen.findByText('the list')).toBeTruthy();
  });
});

describe('editing', () => {
  it('sends only the changed field, and never the type', async () => {
    renderPage();
    const name = (await screen.findByLabelText('Name')) as HTMLInputElement;
    fireEvent.change(name, { target: { value: 'Renamed' } });
    const save = screen.getByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);
    await waitFor(() => expect(sent.some((s) => s.op === 'UpdateIntegration')).toBe(true));
    const u = sent.find((s) => s.op === 'UpdateIntegration')!;
    expect(u.variables).toEqual({ token: 'eu868-gw', request: { name: 'Renamed' } });
    // The type control is shown disabled: it is fixed at creation.
    expect((document.getElementById('ig-type') as HTMLButtonElement).disabled).toBe(true);
  });
});
