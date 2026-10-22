// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the REAL i18n catalogs, confirm dialog, API module and query
// documents. Only the GraphQL transport and the update-access hook are faked, so the copy
// asserted is the copy an operator reads and the variables asserted go on the wire.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, access } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  access: { value: { status: 'available', present: true, canRead: true, canWrite: true, canTrust: true } as Record<string, unknown> },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/lib/updateAuthority', () => ({ useUpdateAccess: () => access.value }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import SignersPage from './SignersPage';

const PRIVATE_PEM = '-----BEGIN PRIVATE KEY-----\nSECRETSECRETSECRET\n-----END PRIVATE KEY-----';
const PUBLIC_PEM = '-----BEGIN PUBLIC KEY-----\nMFkwEwYH\n-----END PUBLIC KEY-----';

function signer(over: Record<string, unknown> = {}) {
  return {
    id: 'id-1',
    createdAt: '2026-08-12T12:00:00Z',
    token: 'signer-aaaa',
    name: 'Release pipeline',
    algorithm: 'ED25519',
    publicKeyPem: PUBLIC_PEM,
    keyId: 'aaaabbbbccccdddd',
    state: 'ACTIVE',
    revokedAt: null,
    createdBy: 'alice',
    revokedBy: null,
    soleSignedArtifactCount: 3,
    ...over,
  };
}

let listed: Record<string, unknown>[] = [];
let revokeError: Error | null = null;
let registerError: Error | null = null;

function ops(name: string) {
  return gqlMock.mock.calls.filter((c) => String(c[1]).includes(name));
}

beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  listed = [signer()];
  revokeError = null;
  registerError = null;
  access.value = { status: 'available', present: true, canRead: true, canWrite: true, canTrust: true };
  gqlMock.mockImplementation((_area: string, doc: unknown, vars: Record<string, unknown>) => {
    const text = String(doc);
    if (text.includes('RegisterArtifactSigner')) {
      return registerError ? Promise.reject(registerError) : Promise.resolve({ registerArtifactSigner: signer({ name: vars.name }) });
    }
    if (text.includes('RevokeArtifactSigner')) {
      return revokeError ? Promise.reject(revokeError) : Promise.resolve({ revokeArtifactSigner: signer({ state: 'REVOKED' }) });
    }
    return Promise.resolve({
      artifactSigners: { results: listed, pagination: { pageStart: 0, pageEnd: listed.length, totalRecords: listed.length } },
    });
  });
});
afterEach(cleanup);

function renderPage() {
  return render(
    <ConfirmProvider>
      <SignersPage />
    </ConfirmProvider>,
  );
}

describe('SignersPage', () => {
  it('shows a loading state, then the signer columns', async () => {
    renderPage();
    expect(screen.getByText('Loading signers…')).toBeTruthy();
    expect(await screen.findByText('Release pipeline')).toBeTruthy();
    expect(screen.getByText('aaaabbbbccccdddd')).toBeTruthy();
    expect(screen.getByText('ED25519')).toBeTruthy();
    expect(screen.getByText('Active')).toBeTruthy();
    expect(screen.getByText('3')).toBeTruthy();
    // The server's pages start at 1 (rdb.ListOf reads anything below 1 as 1), so the first
    // request asks for page 1 — page 0 would make "Next" re-read the first page.
    expect(ops('ArtifactSigners')[0][2]).toEqual({ criteria: { pageNumber: 1, pageSize: 20 } });
  });

  it('explains what a signer is and that only the public key is stored', async () => {
    renderPage();
    await screen.findByText('Release pipeline');
    expect(screen.getByText(/Only the public key is stored here/)).toBeTruthy();
    const link = screen.getByRole('link', { name: /compute its key ID/ });
    expect(link.getAttribute('href')).toContain('signing-updates');
  });

  it('shows the empty state', async () => {
    listed = [];
    renderPage();
    expect(await screen.findByText('No signers are registered yet.')).toBeTruthy();
  });

  it('shows the error state when the list fails', async () => {
    gqlMock.mockImplementation(() => Promise.reject(new Error('boom')));
    renderPage();
    expect(await screen.findByText('Failed to load data.')).toBeTruthy();
  });

  it('says the area is not deployed when absent, without querying', () => {
    access.value = { status: 'absent', present: false, canRead: false, canWrite: false, canTrust: false };
    renderPage();
    expect(screen.getByTestId('area-unavailable')).toBeTruthy();
    expect(gqlMock).not.toHaveBeenCalled();
  });

  it('hides every action from a reader without artifact:trust', async () => {
    access.value = { status: 'available', present: true, canRead: true, canWrite: true, canTrust: false };
    renderPage();
    await screen.findByText('Release pipeline');
    expect(screen.queryByRole('button', { name: 'Register signer' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Revoke' })).toBeNull();
  });

  it('offers revoke only on active signers', async () => {
    listed = [signer(), signer({ id: 'id-2', token: 'signer-bbbb', name: 'Old key', state: 'REVOKED', revokedAt: '2026-09-01T00:00:00Z' })];
    renderPage();
    await screen.findByText('Old key');
    expect(screen.getAllByRole('button', { name: 'Revoke' })).toHaveLength(1);
  });

  it('revoke confirmation states the unverified count and the pinned-signature promise, then revokes', async () => {
    renderPage();
    await screen.findByText('Release pipeline');
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
    expect(await screen.findByText(/3 artifacts have no other active signer and become UNVERIFIED/)).toBeTruthy();
    expect(screen.getByText(/already sent keep the signature they were pinned with/)).toBeTruthy();
    expect(ops('RevokeArtifactSigner')).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: 'Revoke signer' }));
    await waitFor(() => expect(ops('RevokeArtifactSigner')).toHaveLength(1));
    expect(ops('RevokeArtifactSigner')[0][2]).toEqual({ token: 'signer-aaaa' });
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Revoked signer “Release pipeline”.'));
    // The list is re-read after the revoke.
    await waitFor(() => expect(ops('ArtifactSigners').length).toBeGreaterThan(1));
  });

  it('cancelling the revoke confirmation sends nothing', async () => {
    renderPage();
    await screen.findByText('Release pipeline');
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByText(/become UNVERIFIED/)).toBeNull());
    expect(ops('RevokeArtifactSigner')).toHaveLength(0);
  });

  it('shows a revoke failure verbatim', async () => {
    revokeError = new Error('signer already revoked');
    renderPage();
    await screen.findByText('Release pipeline');
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke signer' }));
    expect(await screen.findByText('signer already revoked')).toBeTruthy();
  });
});

describe('RegisterSignerForm via SignersPage', () => {
  async function openForm() {
    renderPage();
    await screen.findByText('Release pipeline');
    fireEvent.click(screen.getByRole('button', { name: 'Register signer' }));
    return {
      name: (await screen.findByLabelText('Name')) as HTMLInputElement,
      pem: (await screen.findByLabelText('Public key (PEM)')) as HTMLTextAreaElement,
    };
  }

  it('refuses a private key client-side, clears the field and never echoes it', async () => {
    const consoleSpies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m));
    const { name, pem } = await openForm();
    fireEvent.change(name, { target: { value: 'k' } });
    fireEvent.change(pem, { target: { value: PRIVATE_PEM } });
    fireEvent.click(screen.getByRole('button', { name: 'Register' }));
    expect(await screen.findByText(/looks like a private key/)).toBeTruthy();
    expect(pem.value).toBe('');
    expect(ops('RegisterArtifactSigner')).toHaveLength(0);
    expect(document.body.textContent).not.toContain('SECRETSECRETSECRET');
    expect(toastMock).not.toHaveBeenCalled();
    for (const spy of consoleSpies) {
      expect(spy.mock.calls.flat().map(String).join(' ')).not.toContain('SECRETSECRETSECRET');
      spy.mockRestore();
    }
  });

  it('registers a public key and refreshes the list', async () => {
    const { name, pem } = await openForm();
    fireEvent.change(name, { target: { value: 'CI key' } });
    fireEvent.change(pem, { target: { value: PUBLIC_PEM } });
    fireEvent.click(screen.getByRole('button', { name: 'Register' }));
    await waitFor(() => expect(ops('RegisterArtifactSigner')).toHaveLength(1));
    expect(ops('RegisterArtifactSigner')[0][2]).toEqual({ name: 'CI key', publicKeyPem: PUBLIC_PEM });
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Registered signer “CI key”.'));
  });

  it('shows the server refusal verbatim and keeps the form', async () => {
    registerError = new Error('SIGNER_ALREADY_REGISTERED: that key is already registered');
    const { name, pem } = await openForm();
    fireEvent.change(name, { target: { value: 'CI key' } });
    fireEvent.change(pem, { target: { value: PUBLIC_PEM } });
    fireEvent.click(screen.getByRole('button', { name: 'Register' }));
    expect(await screen.findByText('SIGNER_ALREADY_REGISTERED: that key is already registered')).toBeTruthy();
    expect(pem.value).toBe(PUBLIC_PEM);
  });
});
