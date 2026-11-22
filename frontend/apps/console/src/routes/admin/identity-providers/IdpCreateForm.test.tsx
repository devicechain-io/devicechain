// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The create-connection form on its own (IdentityProviders.test.tsx drives the common paths
// through the list page): the starting state, every refusal the form makes before sending, the
// claims editor, the exact request for a fully filled form, and what a server refusal leaves
// behind. Only the API call is faked; the form and its request builder run for real.
import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ createIdpConnection: vi.fn() }));
vi.mock('@/lib/api/identity-providers', () => ({ createIdpConnection: api.createIdpConnection }));

import { IdpCreateForm } from './IdpCreateForm';

const CERT = '-----BEGIN CERTIFICATE-----\nMIIBabc\n-----END CERTIFICATE-----';

beforeEach(() => {
  // jsdom has no ResizeObserver; Radix's checkbox measures its form-bubbling input with one.
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  api.createIdpConnection.mockReset();
  api.createIdpConnection.mockResolvedValue({ token: 'acme-sso' });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const fill = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
const create = () => screen.getByRole('button', { name: 'Create connection' });

function fillValid() {
  fill('Token', 'acme-sso');
  fill('Name', 'Acme SSO');
  fill('Issuer', 'https://login.example.com');
  fill('Client ID', 'client-1');
}

describe('IdpCreateForm', () => {
  it('starts as an empty tenant connection with a masked secret and no claims', () => {
    render(<IdpCreateForm onDone={vi.fn()} />);
    expect(screen.getByRole('radio', { name: 'Tenant' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Instance' }).getAttribute('aria-checked')).toBe('false');
    for (const label of ['Token', 'Name', 'Issuer', 'Client ID', 'Scopes', 'Email claim']) {
      expect((screen.getByLabelText(label) as HTMLInputElement).value).toBe('');
    }
    const secret = screen.getByLabelText('Client secret') as HTMLInputElement;
    expect(secret.type).toBe('password');
    expect(secret.autocomplete).toBe('new-password');
    expect(screen.queryByLabelText('Claim name')).toBeNull();
    // The acknowledgement only exists for an instance connection.
    expect(screen.queryByRole('checkbox', { name: /enforces multi-factor/i })).toBeNull();
  });

  it('refuses each missing or malformed field, in order, with its message and sends nothing', async () => {
    render(<IdpCreateForm onDone={vi.fn()} />);
    const expectRefusal = async (message: string) => {
      fireEvent.click(create());
      expect((await screen.findAllByText(message)).length).toBeGreaterThan(0);
      expect(api.createIdpConnection).not.toHaveBeenCalled();
    };

    await expectRefusal('Token is required.');
    fill('Token', 'acme sso');
    await expectRefusal('Token may contain only letters, digits and hyphens.');
    fill('Token', 'acme-sso');
    await expectRefusal('Name is required.');
    fill('Name', 'Acme SSO');
    await expectRefusal('Issuer is required.');
    fill('Issuer', 'http://login.example.com');
    await expectRefusal('Issuer must be an https address.');
    fill('Issuer', 'https://login.example.com');
    await expectRefusal('Client ID is required.');
    fill('Client ID', 'client-1');

    fireEvent.click(screen.getByRole('button', { name: 'Add claim' }));
    fill('Claim name', 'hd');
    await expectRefusal('Every required claim needs both a name and a value.');
    fireEvent.click(screen.getByRole('button', { name: 'Remove claim' }));

    fill('Private CA certificates', '-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----');
    expect(await screen.findAllByText(/contains a private key/)).not.toHaveLength(0);
    await expectRefusal('That text contains a private key. Remove it and supply the CA certificate only; keep the key private.');
  });

  it('adds and removes required claims, one row each', () => {
    render(<IdpCreateForm onDone={vi.fn()} />);
    const add = screen.getByRole('button', { name: 'Add claim' });
    fireEvent.click(add);
    fireEvent.click(add);
    expect(screen.getAllByLabelText('Claim name')).toHaveLength(2);
    fireEvent.change(screen.getAllByLabelText('Claim name')[0], { target: { value: 'hd' } });
    fireEvent.change(screen.getAllByLabelText('Claim name')[1], { target: { value: 'tid' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove claim' })[0]);
    expect(screen.getAllByLabelText('Claim name')).toHaveLength(1);
    expect((screen.getByLabelText('Claim name') as HTMLInputElement).value).toBe('tid');
  });

  it('sends exactly the request a fully filled form describes, trimmed and split', async () => {
    const onDone = vi.fn();
    render(<IdpCreateForm onDone={onDone} />);
    fill('Token', ' acme-sso ');
    fill('Name', ' Acme SSO ');
    fill('Issuer', ' https://login.example.com ');
    fill('Client ID', ' client-1 ');
    fill('Client secret', 's3cret');
    fill('Scopes', 'openid, email  groups');
    fill('Email claim', ' upn ');
    fireEvent.click(screen.getByRole('button', { name: 'Add claim' }));
    fill('Claim name', ' hd ');
    fill('Claim value', ' acme.example ');
    fill('Required authentication methods', 'mfa hwk');
    fill('Private CA certificates', CERT);
    fireEvent.click(create());

    await waitFor(() => expect(onDone).toHaveBeenCalledWith('acme-sso'));
    expect(api.createIdpConnection.mock.calls).toEqual([
      [
        {
          token: 'acme-sso',
          name: 'Acme SSO',
          kind: 'tenant',
          issuer: 'https://login.example.com',
          clientId: 'client-1',
          clientSecret: 's3cret',
          scopes: ['openid', 'email', 'groups'],
          emailClaim: 'upn',
          requiredClaims: [{ name: 'hd', value: 'acme.example' }],
          requiredAmr: ['mfa', 'hwk'],
          caBundle: CERT,
        },
      ],
    ]);
  });

  it('omits what was left blank, so the server defaults apply and a public client sends no secret', async () => {
    render(<IdpCreateForm onDone={vi.fn()} />);
    fillValid();
    fireEvent.click(create());
    await waitFor(() => expect(api.createIdpConnection).toHaveBeenCalledTimes(1));
    expect(api.createIdpConnection.mock.calls[0][0]).toEqual({
      token: 'acme-sso',
      name: 'Acme SSO',
      kind: 'tenant',
      issuer: 'https://login.example.com',
      clientId: 'client-1',
    });
  });

  it('does not send a stale MFA acknowledgement after switching back to a tenant connection', async () => {
    render(<IdpCreateForm onDone={vi.fn()} />);
    fillValid();
    fireEvent.click(screen.getByRole('radio', { name: 'Instance' }));
    fireEvent.click(screen.getByRole('checkbox', { name: /enforces multi-factor/i }));
    fireEvent.click(screen.getByRole('radio', { name: 'Tenant' }));
    fireEvent.click(create());
    await waitFor(() => expect(api.createIdpConnection).toHaveBeenCalledTimes(1));
    const request = api.createIdpConnection.mock.calls[0][0];
    expect(request.kind).toBe('tenant');
    expect(request).not.toHaveProperty('mfaEnforcedByIdp');
  });

  it("shows the server's refusal as it came, keeps the input and does not report done", async () => {
    api.createIdpConnection.mockRejectedValue(
      new GraphQLRequestError('provider unreachable: dial tcp: i/o timeout', 200),
    );
    const onDone = vi.fn();
    render(<IdpCreateForm onDone={onDone} />);
    fillValid();
    fill('Client secret', 's3cret');
    fireEvent.click(create());
    expect(await screen.findByText(/provider unreachable/)).toBeTruthy();
    expect(onDone).not.toHaveBeenCalled();
    expect((screen.getByLabelText('Issuer') as HTMLInputElement).value).toBe('https://login.example.com');
    expect((screen.getByLabelText('Client secret') as HTMLInputElement).value).toBe('s3cret');
    expect((create() as HTMLButtonElement).disabled).toBe(false);
  });
});
