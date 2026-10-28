// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The trust anchors list and its create drawer: paste a CA certificate, see the server's
// validation errors as sentences, and never send a private key. Only the transport is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  gql: vi.fn(),
  toast: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import TrustAnchorsPage from './TrustAnchorsPage';

const CERT = '-----BEGIN CERTIFICATE-----\nMIIBabc\n-----END CERTIFICATE-----';

const ANCHOR = {
  id: 'ta-1',
  token: 'acme-ca',
  name: 'Acme device CA',
  description: null,
  certificatePem: CERT,
  fingerprintSha256: 'ab12',
  subject: 'CN=Acme Device CA',
  issuer: 'CN=Acme Root',
  notBefore: '2026-01-01T00:00:00Z',
  notAfter: '2036-01-01T00:00:00Z',
  enabled: true,
  credentialCount: 2,
  profileCount: 1,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
};

let anchors: unknown[] = [];
let createError: Error | null = null;
let sent: { op: string; variables: Record<string, unknown> }[] = [];

function install() {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: Record<string, unknown>) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'DeviceTrustAnchors':
        return Promise.resolve({
          deviceTrustAnchors: {
            results: anchors,
            pagination: { pageStart: 1, pageEnd: anchors.length, totalRecords: anchors.length },
          },
        });
      case 'DeviceTrustAnchorByToken':
        return Promise.resolve({ deviceTrustAnchorsByToken: [] });
      case 'CreateDeviceTrustAnchor':
        if (createError) return Promise.reject(createError);
        anchors = [ANCHOR];
        return Promise.resolve({ createDeviceTrustAnchor: { ...ANCHOR } });
      default:
        return Promise.resolve({});
    }
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/trust-anchors']}>
      <Routes>
        <Route path="/trust-anchors" element={<TrustAnchorsPage />} />
        <Route path="/trust-anchors/:token" element={<p>the detail</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const refusal = (code: string, message: string) =>
  new GraphQLRequestError(message, 200, [{ message, extensions: { code } }]);

async function openDrawer() {
  fireEvent.click(await screen.findByRole('button', { name: 'New trust anchor' }));
  return screen.findByLabelText('Token');
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  anchors = [];
  createError = null;
  install();
});
afterEach(cleanup);

describe('the list', () => {
  it('shows each anchor with its subject, expiry and in-use counts, linking to the detail page', async () => {
    anchors = [ANCHOR];
    renderPage();
    expect(await screen.findByText('Acme device CA')).toBeTruthy();
    expect(screen.getByText('CN=Acme Device CA')).toBeTruthy();
    expect(screen.getByText('Credentials 2 · Profiles 1')).toBeTruthy();
    fireEvent.click(screen.getByText('Acme device CA'));
    expect(await screen.findByText('the detail')).toBeTruthy();
  });

  it('has an empty state that says what an anchor is for', async () => {
    renderPage();
    expect(await screen.findByText(/No trust anchors yet/)).toBeTruthy();
  });

  it('hides the create button from a member who cannot write', async () => {
    h.claims = { username: 'ro@oem.example', authorities: ['device:read'], scopeAnchors: [] };
    renderPage();
    await screen.findByText(/No trust anchors yet/);
    expect(screen.queryByRole('button', { name: 'New trust anchor' })).toBeNull();
  });
});

describe('creating an anchor', () => {
  const fill = async (pem: string) => {
    fireEvent.change(await openDrawer(), { target: { value: 'acme-ca' } });
    fireEvent.change(screen.getByLabelText('CA certificate (PEM)'), { target: { value: pem } });
  };
  const create = () => screen.getByRole('button', { name: 'Create trust anchor' }) as HTMLButtonElement;

  it('sends the pasted certificate and refreshes the list', async () => {
    renderPage();
    await fill(CERT);
    await waitFor(() => expect(create().disabled).toBe(false));
    fireEvent.click(create());
    await waitFor(() => expect(sent.filter((s) => s.op === 'CreateDeviceTrustAnchor')).toHaveLength(1));
    expect(sent.find((s) => s.op === 'CreateDeviceTrustAnchor')!.variables).toEqual({
      request: { token: 'acme-ca', name: null, certificatePem: CERT },
    });
    expect(await screen.findByText('Acme device CA')).toBeTruthy();
  });

  it('refuses a private key in the browser: says so and keeps the button off', async () => {
    renderPage();
    await fill('-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----');
    expect(await screen.findByText(/contains a private key/)).toBeTruthy();
    expect(create().disabled).toBe(true);
    expect(sent.filter((s) => s.op === 'CreateDeviceTrustAnchor')).toHaveLength(0);
  });

  it('requires exactly one certificate', async () => {
    renderPage();
    await fill(`${CERT}\n${CERT}`);
    expect(await screen.findByText(/exactly one certificate/)).toBeTruthy();
    expect(create().disabled).toBe(true);
  });

  it('fills the box from a chosen file', async () => {
    renderPage();
    await openDrawer();
    const input = document.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File([CERT], 'ca.pem', { type: 'text/plain' });
    Object.defineProperty(file, 'text', { value: () => Promise.resolve(CERT) });
    fireEvent.change(input, { target: { files: [file] } });
    await waitFor(() => expect((screen.getByLabelText('CA certificate (PEM)') as HTMLTextAreaElement).value).toBe(CERT));
  });

  it("shows the server's validation error with its detail and keeps the drawer open", async () => {
    createError = refusal('INVALID_VALUE', 'the certificate is not a CA: it lacks the CA basic constraint');
    renderPage();
    await fill(CERT);
    await waitFor(() => expect(create().disabled).toBe(false));
    fireEvent.click(create());
    expect(await screen.findByText(/The request was refused\. the certificate is not a CA/)).toBeTruthy();
    expect(screen.getByLabelText('CA certificate (PEM)')).toBeTruthy();
  });

  it('explains a duplicate and the per-tenant limit', async () => {
    createError = refusal('CONFLICT', 'duplicate key value');
    renderPage();
    await fill(CERT);
    await waitFor(() => expect(create().disabled).toBe(false));
    fireEvent.click(create());
    expect(await screen.findByText(/already exists/)).toBeTruthy();
    cleanup();

    createError = refusal('LIMIT_EXCEEDED', 'limit exceeded for trust anchors: 33 requested, at most 32 allowed');
    renderPage();
    await fill(CERT);
    await waitFor(() => expect(create().disabled).toBe(false));
    fireEvent.click(create());
    expect(await screen.findByText(/A limit was reached\. limit exceeded for trust anchors/)).toBeTruthy();
  });
});
