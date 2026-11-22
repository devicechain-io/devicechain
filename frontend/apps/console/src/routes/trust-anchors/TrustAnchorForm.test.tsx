// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The trust-anchor create form on its own (the list page's drawer test drives it through the
// page): what it starts as, which input keeps the button off, the exact request, what it hands
// back, and a server refusal that leaves the typed input where it was.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { TrustAnchorForm } from './TrustAnchorForm';

const CERT = '-----BEGIN CERTIFICATE-----\nMIIBabc\n-----END CERTIFICATE-----';

let createError: Error | null = null;

function creates(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((c) => /CreateDeviceTrustAnchor/.test(String(c[1])))
    .map((c) => (c[2] as { request: Record<string, unknown> }).request);
}

afterEach(cleanup);
beforeEach(() => {
  createError = null;
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, doc: unknown) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    if (/CreateDeviceTrustAnchor/.test(String(doc))) {
      if (createError) return Promise.reject(createError);
      return Promise.resolve({ createDeviceTrustAnchor: { token: 'acme-ca' } });
    }
    return Promise.resolve({ deviceTrustAnchorsByToken: [] });
  });
});

const create = () => screen.getByRole('button', { name: 'Create trust anchor' }) as HTMLButtonElement;
const pem = () => screen.getByLabelText('CA certificate (PEM)') as HTMLTextAreaElement;

describe('TrustAnchorForm', () => {
  it('starts empty with the button off and no complaint about the empty box', () => {
    render(<TrustAnchorForm onCreated={vi.fn()} />);
    expect((screen.getByLabelText('Token') as HTMLInputElement).value).toBe('');
    expect(pem().value).toBe('');
    expect(create().disabled).toBe(true);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('needs both a token and a certificate before it can be submitted', async () => {
    render(<TrustAnchorForm onCreated={vi.fn()} />);
    fireEvent.change(pem(), { target: { value: CERT } });
    expect(create().disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'acme-ca' } });
    await waitFor(() => expect(create().disabled).toBe(false));
  });

  it('says what is wrong with text that is not one certificate', async () => {
    render(<TrustAnchorForm onCreated={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'acme-ca' } });

    fireEvent.change(pem(), { target: { value: 'hello' } });
    expect((await screen.findByRole('alert')).textContent).toMatch(/certificate/i);
    expect(create().disabled).toBe(true);

    fireEvent.change(pem(), { target: { value: '-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----' } });
    expect((await screen.findByRole('alert')).textContent).toMatch(/private key/);
    expect(create().disabled).toBe(true);

    fireEvent.change(pem(), { target: { value: `${CERT}\n${CERT}` } });
    expect((await screen.findByRole('alert')).textContent).toMatch(/exactly one certificate/);
    expect(create().disabled).toBe(true);
    expect(creates()).toHaveLength(0);
  });

  it('sends the trimmed token, the name and the trimmed certificate, then hands back the token', async () => {
    const onCreated = vi.fn();
    render(<TrustAnchorForm onCreated={onCreated} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: ' acme-ca ' } });
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: ' Acme device CA ' } });
    fireEvent.change(pem(), { target: { value: `\n${CERT}\n\n` } });
    await waitFor(() => expect(create().disabled).toBe(false));
    fireEvent.click(create());
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith('acme-ca'));
    expect(creates()).toEqual([{ token: 'acme-ca', name: 'Acme device CA', certificatePem: CERT }]);
  });

  it('shows the server refusal and keeps what was typed', async () => {
    createError = new GraphQLRequestError('certificate has expired', 200, [
      { message: 'certificate has expired', extensions: { code: 'INVALID_VALUE' } },
    ]);
    const onCreated = vi.fn();
    render(<TrustAnchorForm onCreated={onCreated} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'acme-ca' } });
    fireEvent.change(pem(), { target: { value: CERT } });
    await waitFor(() => expect(create().disabled).toBe(false));
    fireEvent.click(create());
    expect(await screen.findByText(/certificate has expired/)).toBeTruthy();
    expect(onCreated).not.toHaveBeenCalled();
    expect(pem().value).toBe(CERT);
    expect((screen.getByLabelText('Token') as HTMLInputElement).value).toBe('acme-ca');
    expect(create().disabled).toBe(false);
  });
});
