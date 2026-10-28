// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A trust anchor's detail page, and above all its two guards:
//   * DISABLE names what it does to the fleet (the devices relying on the anchor are
//     disconnected) before anything is sent, and sends nothing when the operator declines;
//   * DELETE of an anchor in use explains why, with the counts, and never reaches the server;
//     a refusal the server gives anyway (the counts on screen were stale) gets the same
//     explanation rather than a raw message.
// Only the transport is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  gql: vi.fn(),
  toast: vi.fn(),
  confirm: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => h.confirm }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import TrustAnchorDetailPage from './TrustAnchorDetailPage';

const BASE = {
  id: 'ta-1',
  token: 'acme-ca',
  name: 'Acme device CA',
  description: null,
  certificatePem: '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----',
  fingerprintSha256: 'ab12cd34ef56',
  subject: 'CN=Acme Device CA,O=Acme',
  issuer: 'CN=Acme Root,O=Acme',
  notBefore: '2026-01-01T00:00:00Z',
  notAfter: '2036-01-01T00:00:00Z',
  enabled: true,
  credentialCount: 0,
  profileCount: 0,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
};

let anchor: typeof BASE;
let sent: { op: string; variables: Record<string, unknown> }[] = [];
let deleteError: Error | null = null;
let updateError: Error | null = null;
const writes = (op: string) => sent.filter((s) => s.op === op);

function install() {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: Record<string, unknown>) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'DeviceTrustAnchorByToken':
        return Promise.resolve({ deviceTrustAnchorsByToken: [anchor] });
      case 'SetDeviceTrustAnchorEnabled':
        if (updateError) return Promise.reject(updateError);
        anchor = { ...anchor, enabled: (variables.request as { enabled: boolean }).enabled };
        return Promise.resolve({ updateDeviceTrustAnchor: { id: anchor.id, token: anchor.token, enabled: anchor.enabled } });
      case 'DeleteDeviceTrustAnchor':
        return deleteError ? Promise.reject(deleteError) : Promise.resolve({ deleteDeviceTrustAnchor: true });
      default:
        return Promise.resolve({});
    }
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/trust-anchors/acme-ca']}>
      <Routes>
        <Route path="/trust-anchors/:token" element={<TrustAnchorDetailPage />} />
        <Route path="/trust-anchors" element={<p>the list</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  h.confirm.mockReset();
  h.confirm.mockResolvedValue(true);
  anchor = { ...BASE };
  deleteError = null;
  updateError = null;
  install();
});
afterEach(cleanup);

describe('the anchor summary', () => {
  it('shows the fingerprint, subject, validity and in-use counts', async () => {
    anchor = { ...anchor, credentialCount: 3, profileCount: 1 };
    renderPage();
    expect(await screen.findByText('ab12cd34ef56')).toBeTruthy();
    expect(screen.getByText('CN=Acme Device CA,O=Acme')).toBeTruthy();
    expect(screen.getByText('CN=Acme Root,O=Acme')).toBeTruthy();
    expect(screen.getByText('3')).toBeTruthy();
    expect(screen.getByText('1')).toBeTruthy();
  });

  it('badges a disabled anchor and says what that means', async () => {
    anchor = { ...anchor, enabled: false };
    renderPage();
    expect(await screen.findByText(/No certificate chaining to it authenticates/)).toBeTruthy();
    expect(screen.getAllByText('Disabled').length).toBeGreaterThan(0);
  });

  it('offers no actions to a member who cannot write', async () => {
    h.claims = { username: 'ro@oem.example', authorities: ['device:read'], scopeAnchors: [] };
    renderPage();
    await screen.findByText('ab12cd34ef56');
    expect(screen.queryByRole('button', { name: /Disable/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Delete/ })).toBeNull();
  });
});

describe('disabling the anchor', () => {
  it('warns that the devices relying on it will be disconnected, with how many rely on it', async () => {
    anchor = { ...anchor, credentialCount: 4, profileCount: 2 };
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Disable/ }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalledTimes(1));
    const options = h.confirm.mock.calls[0][0] as { title: string; description: string };
    expect(options.title).toBe('Disable trust anchor');
    expect(options.description).toContain('disconnected');
    expect(options.description).toContain('cannot reconnect');
    expect(options.description).toContain('4 credential(s) and 2 provisioning profile(s)');
  });

  it('sends nothing when the operator declines', async () => {
    h.confirm.mockResolvedValue(false);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Disable/ }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalled());
    expect(writes('SetDeviceTrustAnchorEnabled')).toHaveLength(0);
  });

  it('sends only enabled:false once confirmed, and shows the anchor disabled', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Disable/ }));
    await waitFor(() => expect(writes('SetDeviceTrustAnchorEnabled')).toHaveLength(1));
    expect(writes('SetDeviceTrustAnchorEnabled')[0].variables).toEqual({ token: 'acme-ca', request: { enabled: false } });
    expect(await screen.findByRole('button', { name: /Enable/ })).toBeTruthy();
  });

  it('enables without a confirmation: enabling disconnects no one', async () => {
    anchor = { ...anchor, enabled: false };
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Enable/ }));
    await waitFor(() => expect(writes('SetDeviceTrustAnchorEnabled')).toHaveLength(1));
    expect(h.confirm).not.toHaveBeenCalled();
    expect(writes('SetDeviceTrustAnchorEnabled')[0].variables).toEqual({ token: 'acme-ca', request: { enabled: true } });
  });

  it('shows the server sentence when the disable was saved but the sessions were not confirmed ended, and reloads', async () => {
    updateError = new Error('the change was saved, but the sessions could not be confirmed ended');
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Disable/ }));
    expect(await screen.findByText(/could not be confirmed ended/)).toBeTruthy();
    // The page re-read the stored state rather than assuming the call changed nothing.
    await waitFor(() => expect(writes('DeviceTrustAnchorByToken').length).toBeGreaterThan(1));
  });
});

describe('deleting the anchor', () => {
  it('explains why an anchor in use cannot be deleted, with the counts, and never asks the server', async () => {
    anchor = { ...anchor, credentialCount: 2, profileCount: 1 };
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Delete/ }));
    expect(
      await screen.findByText(/cannot be deleted while it is in use: 2 credential\(s\) and 1 provisioning profile\(s\) still name it/),
    ).toBeTruthy();
    expect(h.confirm).not.toHaveBeenCalled();
    expect(writes('DeleteDeviceTrustAnchor')).toHaveLength(0);
  });

  it('blocks on provisioning profiles alone', async () => {
    anchor = { ...anchor, credentialCount: 0, profileCount: 3 };
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Delete/ }));
    expect(await screen.findByText(/0 credential\(s\) and 3 provisioning profile\(s\)/)).toBeTruthy();
    expect(writes('DeleteDeviceTrustAnchor')).toHaveLength(0);
  });

  it('deletes an unused anchor after confirmation and returns to the list', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Delete/ }));
    expect(await screen.findByText('the list')).toBeTruthy();
    expect(h.confirm).toHaveBeenCalledTimes(1);
    expect(writes('DeleteDeviceTrustAnchor')[0].variables).toEqual({ token: 'acme-ca' });
  });

  it('explains a refusal the server gives when the counts on screen were stale', async () => {
    deleteError = new GraphQLRequestError('entity is still referenced', 200, [
      { message: 'entity is still referenced', extensions: { code: 'REFERENCE_VIOLATION' } },
    ]);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Delete/ }));
    expect(await screen.findByText(/still in use by a credential or a provisioning profile/)).toBeTruthy();
    expect(screen.queryByText('the list')).toBeNull();
  });
});
