// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The integrations list and its create flow, with the reveal-once bearer token.
//
// 🔴 What is proven about the token, and how. Only the transport is faked, so the page, the form,
// the dialog and the API wrappers are real. The fake returns the token in exactly one response
// (the create mutation) and in no other; the assertions then read BOTH sides:
//   * the DOM: the token is on screen while the dialog is open (in its field and in the connect
//     snippet's header), and gone from the document once it is closed;
//   * the wire: nothing the page sends afterwards selects `issuedToken`, so there is no request
//     that could bring the value back, and nothing sent carries the value.
// The positive controls (it IS shown, it IS copyable) are what make the negative ones mean
// anything.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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

import IntegrationsPage from './IntegrationsPage';

const TOKEN = 'B3arer-shown-once-0123456789abcdefghijklmnopq';

const INTEGRATION = {
  id: 'i-1',
  token: 'eu868-gw',
  name: 'EU868 gateway fleet',
  description: null,
  type: 'CHIRPSTACK_V4',
  enabled: true,
  identity: 'DEV_EUI',
  generic: null,
  linkMetrics: false,
  tokenIssuedAt: '2026-10-01T00:00:00Z',
  undecodedCount: 0,
  lastUndecodedAt: null,
  lastUndecodedSample: null,
  lastAuthFailureAt: null,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  deviceTypes: [{ id: 'dt-1', token: 'meter', name: 'Meter' }],
};

const DEVICE_TYPES = [
  { id: 'dt-1', token: 'meter', name: 'Meter', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null },
  { id: 'dt-2', token: 'pump', name: 'Pump', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null },
];

/** The documents the page sent, by operation name, in order. */
let sent: { op: string; text: string; variables: any }[] = [];
let integrations: unknown[] = [];

function install() {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: any) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const text = String(doc);
    const op = /(?:query|mutation)\s+(\w+)/.exec(text)?.[1] ?? '';
    sent.push({ op, text, variables });
    switch (op) {
      case 'Integrations':
        return Promise.resolve({
          integrations: {
            results: integrations,
            pagination: { pageStart: 1, pageEnd: integrations.length, totalRecords: integrations.length },
          },
        });
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: { results: DEVICE_TYPES, pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 } },
        });
      case 'IntegrationByToken':
        return Promise.resolve({ integration: null });
      case 'CreateIntegration':
        integrations = [INTEGRATION];
        return Promise.resolve({ createIntegration: { issuedToken: TOKEN, integration: INTEGRATION } });
      default:
        return Promise.resolve({});
    }
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/integrations']}>
      <Routes>
        <Route path="/integrations" element={<IntegrationsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

const revealDialog = () => document.querySelector('[data-shot="integration-token"]') as HTMLElement | null;
async function findRevealDialog(): Promise<HTMLElement> {
  await waitFor(() => expect(revealDialog()).not.toBeNull());
  return revealDialog()!;
}

async function openCreate() {
  fireEvent.click(await screen.findByRole('button', { name: 'New integration' }));
  fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'eu868-gw' } });
}

async function chooseScope(...names: string[]) {
  fireEvent.click(document.getElementById('ig-scope') as HTMLButtonElement);
  for (const n of names) fireEvent.click(await screen.findByText(n));
  // Close the picker so the rest of the form is reachable.
  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
}

async function createIntegration() {
  await openCreate();
  await chooseScope('Meter');
  const create = screen.getByRole('button', { name: 'Create integration' });
  await waitFor(() => expect((create as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(create);
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', tenant: 'acme', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  integrations = [];
  install();
});
afterEach(cleanup);

describe('the integrations list', () => {
  it('shows each integration with its type, scope and badges', async () => {
    integrations = [
      INTEGRATION,
      { ...INTEGRATION, id: 'i-2', token: 'legacy', name: null, enabled: false, undecodedCount: 4, lastAuthFailureAt: '2026-10-02T00:00:00Z' },
    ];
    renderPage();
    expect(await screen.findByText('EU868 gateway fleet')).toBeTruthy();
    expect(screen.getAllByText('ChirpStack v4').length).toBe(2);
    expect(screen.getAllByText('Meter').length).toBe(2);
    expect(screen.getByText('Disabled')).toBeTruthy();
    expect(screen.getByText('Rejected token')).toBeTruthy();
    expect(screen.getByText('4 undecoded')).toBeTruthy();
  });

  it('offers no create to a member without device:write', async () => {
    h.claims = { username: 'reader@oem.example', tenant: 'acme', authorities: ['device:read'], scopeAnchors: [] };
    integrations = [INTEGRATION];
    renderPage();
    await screen.findByText('EU868 gateway fleet');
    expect(screen.queryByRole('button', { name: 'New integration' })).toBeNull();
  });

  it('offers no create to a member limited to part of the tenant, even holding device:write', async () => {
    h.claims = { username: 'cust@acme.example', tenant: 'acme', authorities: ['*'], scopeAnchors: ['customer:acme'] };
    integrations = [INTEGRATION];
    renderPage();
    await screen.findByText('EU868 gateway fleet');
    expect(screen.queryByRole('button', { name: 'New integration' })).toBeNull();
  });
});

describe('the scope picker', () => {
  it('blocks create until a device type is chosen, and says why', async () => {
    renderPage();
    await openCreate();
    const create = screen.getByRole('button', { name: 'Create integration' }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    expect(screen.getByText('Choose at least one device type.')).toBeTruthy();

    await chooseScope('Meter', 'Pump');
    await waitFor(() => expect(create.disabled).toBe(false));
  });

  it('sends the chosen device types as the scope', async () => {
    renderPage();
    await openCreate();
    await chooseScope('Meter', 'Pump');
    const create = screen.getByRole('button', { name: 'Create integration' });
    await waitFor(() => expect((create as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(create);
    await waitFor(() => expect(sent.some((s) => s.op === 'CreateIntegration')).toBe(true));
    const request = sent.find((s) => s.op === 'CreateIntegration')!.variables.request;
    expect(request).toMatchObject({ token: 'eu868-gw', type: 'CHIRPSTACK_V4', deviceTypeTokens: ['meter', 'pump'], identity: 'DEV_EUI' });
    expect(request).not.toHaveProperty('generic');
  });
});

describe('the generic settings form', () => {
  it('blocks create on a malformed pointer and sends the settings document when valid', async () => {
    renderPage();
    await openCreate();
    await chooseScope('Meter');
    fireEvent.click(document.getElementById('ig-type') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Generic JSON'));

    const create = screen.getByRole('button', { name: 'Create integration' }) as HTMLButtonElement;
    // Nothing typed yet: the alias scheme name and the identifier location are required.
    expect(create.disabled).toBe(true);

    fireEvent.change(document.getElementById('ig-alias-name') as HTMLInputElement, { target: { value: 'serial' } });
    fireEvent.change(document.getElementById('ig-id-value') as HTMLInputElement, { target: { value: 'device/id' } });
    expect(create.disabled).toBe(true);
    expect(screen.getByText(/not a valid location/)).toBeTruthy();

    fireEvent.change(document.getElementById('ig-id-value') as HTMLInputElement, { target: { value: '/device/id' } });
    fireEvent.change(document.getElementById('ig-items') as HTMLInputElement, { target: { value: '/uplinks' } });
    await waitFor(() => expect(create.disabled).toBe(false));
    fireEvent.click(create);

    await waitFor(() => expect(sent.some((s) => s.op === 'CreateIntegration')).toBe(true));
    const request = sent.find((s) => s.op === 'CreateIntegration')!.variables.request;
    expect(request.type).toBe('GENERIC_JSON');
    expect(request.identity).toBe('ALIAS:custom.serial');
    expect(JSON.parse(request.generic)).toEqual({ idFrom: 'pointer:/device/id', itemsPointer: '/uplinks' });
  });
});

describe('creating an integration reveals the token exactly once', () => {
  it('shows the token with a copy button and the warning, after the integration is created', async () => {
    renderPage();
    await createIntegration();

    const dialog = await findRevealDialog();
    expect((screen.getByLabelText('Bearer token', { selector: '#ig-reveal-token' }) as HTMLInputElement).value).toBe(TOKEN);
    expect(dialog.textContent).toContain('It is shown only once');
    expect(screen.getByRole('button', { name: 'Copy the bearer token' })).toBeTruthy();
  });

  it('puts the real tenant and the integration token in the URL, and the bearer token only in the header', async () => {
    renderPage();
    await createIntegration();
    await findRevealDialog();

    const inputs = Array.from(revealDialog()!.querySelectorAll('input')).map((i) => i.value);
    // The URL is the console's own origin with the signed-in tenant and the integration's token.
    const url = inputs.find((v) => v.includes('/api/integrations/'))!;
    expect(url).toBe(`${window.location.origin}/api/integrations/acme/eu868-gw`);
    expect(url).not.toContain(TOKEN);
    expect(inputs).toContain('Authorization');
    expect(inputs).toContain(`Bearer ${TOKEN}`);
    expect(inputs).toContain('JSON');
  });

  it('copies the token to the clipboard when asked', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    renderPage();
    await createIntegration();
    fireEvent.click(await screen.findByRole('button', { name: 'Copy the bearer token' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(TOKEN));
  });

  it('is gone from the document once the dialog is closed', async () => {
    renderPage();
    await createIntegration();
    await findRevealDialog();
    expect(Array.from(document.querySelectorAll('input')).some((i) => i.value === TOKEN)).toBe(true);

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));

    await waitFor(() => expect(revealDialog()).toBeNull());
    expect(document.body.innerHTML).not.toContain(TOKEN);
    // The new integration is on the list behind it, so the close lost nothing but the token.
    expect(await screen.findByText('EU868 gateway fleet')).toBeTruthy();
  });

  it('is never in a refetch: nothing sent after the create can return it', async () => {
    renderPage();
    await createIntegration();
    await findRevealDialog();
    // The create reloads the list. Give any other request the chance to fire.
    await waitFor(() => expect(sent.filter((s) => s.op === 'Integrations').length).toBeGreaterThanOrEqual(2));

    // Exactly one document selects the token: the create mutation. The reload does not.
    expect(sent.filter((s) => s.text.includes('issuedToken')).map((s) => s.op)).toEqual(['CreateIntegration']);
    // And no request carries the value back to the server either.
    expect(JSON.stringify(sent.map((s) => s.variables))).not.toContain(TOKEN);
  });

  it('does not close on a stray click or Esc: the token cannot be read again', async () => {
    renderPage();
    await createIntegration();
    const dialog = await findRevealDialog();
    fireEvent.keyDown(dialog, { key: 'Escape' });
    fireEvent.pointerDown(document.body);
    fireEvent.click(document.body);
    expect(revealDialog()).not.toBeNull();
    expect((document.getElementById('ig-reveal-token') as HTMLInputElement).value).toBe(TOKEN);
  });

  it('is cleared by navigating away, not only by closing', async () => {
    const view = renderPage();
    await createIntegration();
    await findRevealDialog();
    view.unmount();
    expect(document.body.innerHTML).not.toContain(TOKEN);
  });
});
