// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. Only the GraphQL transport, the toast
// and the confirm dialog are faked; the panel, its request shaping and the API module
// all run.
//
// 🔴 WHY THIS FILE EXISTS. The platform keeps only a keyed digest of an MQTT_BASIC
// password and of an ACCESS_TOKEN, so the moment either is set is the ONLY moment the
// operator can read it. A
// panel that lost it on the reload that follows the mutation strands the operator with
// a device they cannot program; a panel that rendered a value from a READ would be
// showing a digest or a stale secret. Nothing else notices either.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, confirmMock } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  confirmMock: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => confirmMock }));

import { DeviceCredentialsPanel } from './DeviceCredentialsPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  confirmMock.mockReset();
});

const STRONG = 'correct-horse-battery-staple-1';

// A server that would leak if the console asked: every read carries a credentialValue.
const mqttRow = {
  id: '1',
  token: 'cred-mqtt',
  credentialType: 'MQTT_BASIC',
  credentialId: 'sensor-user',
  credentialValue: 'LEAKED-DIGEST-OR-SECRET',
  enabled: true,
  expiresAt: null,
  createdAt: '2026-01-01T00:00:00Z',
};
const tokenRow = {
  ...mqttRow,
  id: '2',
  token: 'cred-token',
  credentialType: 'ACCESS_TOKEN',
  // What a read of an ACCESS_TOKEN returns: a fingerprint, never the token.
  credentialId: 'dctk1$fingerprint-only',
  credentialValue: 'LEAKED-TOKEN-VALUE',
};

function route(rows: unknown[]) {
  gqlMock.mockImplementation((_s: string, doc: string) => {
    const d = String(doc);
    // The server returns an access token on the response to the write that set it, and only there.
    if (d.includes('mutation CreateDeviceCredential')) {
      return Promise.resolve({ createDeviceCredential: { credentialId: 'minted-token-0001' } });
    }
    if (d.includes('mutation UpdateDeviceCredential')) {
      return Promise.resolve({ updateDeviceCredential: { credentialId: 'rotated-token-0002' } });
    }
    return Promise.resolve({ deviceCredentials: { results: rows, pagination: { totalRecords: rows.length } } });
  });
}

const callsTo = (name: string) => gqlMock.mock.calls.filter((c) => String(c[1]).includes(name));

async function pickMqtt() {
  // The type chooser is a popover Combobox: open it by its current value, pick by text.
  fireEvent.click(screen.getByRole('button', { name: 'ACCESS_TOKEN' }));
  fireEvent.click(await screen.findByText('MQTT_BASIC'));
}

function passwordField(): HTMLInputElement {
  return document.querySelector('input[type="password"]') as HTMLInputElement;
}

async function addMqtt(password: string) {
  await pickMqtt();
  const user = screen.getAllByRole('textbox')[0];
  fireEvent.focus(user);
  fireEvent.change(user, { target: { value: 'sensor-user' } });
  fireEvent.focus(passwordField());
  fireEvent.change(passwordField(), { target: { value: password } });
  fireEvent.click(screen.getByRole('button', { name: /add credential/i }));
}

describe('DeviceCredentialsPanel password handling', () => {
  it('shows the password once after create, with a copy button and the warning', async () => {
    route([]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await addMqtt(STRONG);
    expect((await screen.findByTestId('issued-password')).textContent).toBe(STRONG);
    expect(screen.getByRole('button', { name: 'Copy password' })).toBeTruthy();
    expect(screen.getByText(/won't see this password again/i)).toBeTruthy();
    expect(callsTo('CreateDeviceCredential')[0][2]).toMatchObject({
      request: { credentialType: 'MQTT_BASIC', credentialId: 'sensor-user', credentialValue: STRONG },
    });
  });

  it('keeps the banner through the reload that follows, until dismissed', async () => {
    route([mqttRow]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await addMqtt(STRONG);
    await screen.findByTestId('issued-password');
    await waitFor(() => expect(callsTo('query DeviceCredentials').length).toBeGreaterThan(1));
    expect(screen.getByTestId('issued-password').textContent).toBe(STRONG);
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByTestId('issued-password')).toBeNull();
    expect(document.body.textContent).not.toContain(STRONG);
  });

  it('is gone after navigating away and back (remount)', async () => {
    route([]);
    const first = render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await addMqtt(STRONG);
    await screen.findByTestId('issued-password');
    first.unmount();
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await waitFor(() => expect(screen.queryByText(/loading credentials/i)).toBeNull());
    expect(screen.queryByTestId('issued-password')).toBeNull();
    expect(document.body.textContent).not.toContain(STRONG);
  });

  it('is gone when the panel is shown for a different device', async () => {
    route([]);
    const view = render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await addMqtt(STRONG);
    await screen.findByTestId('issued-password');
    view.rerender(<DeviceCredentialsPanel deviceToken="dev-2" />);
    await waitFor(() => expect(screen.queryByTestId('issued-password')).toBeNull());
  });

  it('shows the new password once after a rotation, sending only credentialValue', async () => {
    route([mqttRow]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    fireEvent.click(await screen.findByRole('button', { name: /set new password/i }));
    const input = screen.getByLabelText('New password') as HTMLInputElement;
    expect(input.value).toBe(''); // never pre-filled
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: STRONG } });
    fireEvent.click(screen.getByRole('button', { name: 'Save password' }));
    expect((await screen.findByTestId('issued-password')).textContent).toBe(STRONG);
    expect(callsTo('UpdateDeviceCredential')[0][2]).toEqual({
      token: 'cred-mqtt',
      request: { credentialValue: STRONG },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByTestId('issued-password')).toBeNull();
  });

  it('shows the server refusal and no banner when a rotation is refused', async () => {
    gqlMock.mockImplementation((_s: string, doc: string) =>
      String(doc).includes('mutation UpdateDeviceCredential')
        ? Promise.reject(new Error('password must be at least 16 bytes'))
        : Promise.resolve({ deviceCredentials: { results: [mqttRow], pagination: { totalRecords: 1 } } }),
    );
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    fireEvent.click(await screen.findByRole('button', { name: /set new password/i }));
    const input = screen.getByLabelText('New password');
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: 'short' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save password' }));
    await waitFor(() =>
      expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('at least 16 bytes'), 'error'),
    );
    expect(screen.queryByTestId('issued-password')).toBeNull();
  });

  it('renders no value from a read, for either type', async () => {
    route([mqttRow, tokenRow]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await screen.findByText('sensor-user');
    expect(document.body.textContent).not.toContain('LEAKED');
    expect(screen.getByText(/password is set/i)).toBeTruthy();
    // The list query never asks for the secret at all.
    expect(String(callsTo('query DeviceCredentials')[0][1])).not.toContain('credentialValue');
    // Opening the rotate form pre-fills nothing.
    fireEvent.click(screen.getByRole('button', { name: /set new password/i }));
    expect((screen.getByLabelText('New password') as HTMLInputElement).value).toBe('');
  });

  it('shows no value for an access token, only that one is set, and offers a new one', async () => {
    route([tokenRow]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    expect(await screen.findByText(/access token is set/i)).toBeTruthy();
    expect(document.body.textContent).not.toContain('dctk1$fingerprint-only');
    expect(screen.queryByRole('button', { name: 'Copy id' })).toBeNull();
    expect(screen.queryByRole('button', { name: /set new password/i })).toBeNull();
    expect(screen.getByRole('button', { name: /set new token/i })).toBeTruthy();
  });

  it('shows a new access token once after create, from the response, and not after dismissal', async () => {
    route([tokenRow]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    fireEvent.click(await screen.findByRole('button', { name: /generate/i }));
    fireEvent.click(screen.getByRole('button', { name: /add credential/i }));
    expect((await screen.findByTestId('issued-token')).textContent).toBe('minted-token-0001');
    expect(screen.getByRole('button', { name: 'Copy access token' })).toBeTruthy();
    expect(callsTo('CreateDeviceCredential')[0][2]).toMatchObject({ request: { credentialType: 'ACCESS_TOKEN' } });
    // The reload that follows reads only a fingerprint; the banner keeps the token until dismissed.
    await waitFor(() => expect(callsTo('query DeviceCredentials').length).toBeGreaterThan(1));
    expect(screen.getByTestId('issued-token').textContent).toBe('minted-token-0001');
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByTestId('issued-token')).toBeNull();
    expect(document.body.textContent).not.toContain('minted-token-0001');
  });

  it('rotates an access token after a confirmation, sending only credentialId, and shows the new one once', async () => {
    route([tokenRow]);
    confirmMock.mockResolvedValue(true);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    fireEvent.click(await screen.findByRole('button', { name: /set new token/i }));
    expect((await screen.findByTestId('issued-token')).textContent).toBe('rotated-token-0002');
    const call = callsTo('UpdateDeviceCredential')[0][2] as { token: string; request: Record<string, unknown> };
    expect(call.token).toBe('cred-token');
    expect(Object.keys(call.request)).toEqual(['credentialId']);
    expect(call.request.credentialId).not.toBe('rotated-token-0002'); // the console generated its own
  });

  it('does not rotate an access token when the confirmation is declined', async () => {
    route([tokenRow]);
    confirmMock.mockResolvedValue(false);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    fireEvent.click(await screen.findByRole('button', { name: /set new token/i }));
    await waitFor(() => expect(confirmMock).toHaveBeenCalled());
    expect(callsTo('UpdateDeviceCredential')).toHaveLength(0);
    expect(screen.queryByTestId('issued-token')).toBeNull();
  });

  it('hints under 16 bytes without blocking, and counts bytes', async () => {
    route([]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await pickMqtt();
    fireEvent.focus(passwordField());
    fireEvent.change(passwordField(), { target: { value: 'short' } });
    expect(screen.getByRole('status').textContent).toMatch(/16 bytes/);
    fireEvent.change(passwordField(), { target: { value: '€'.repeat(6) } }); // 6 characters, 18 bytes
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('generates a strong password into the field', async () => {
    route([]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    await pickMqtt();
    fireEvent.click(screen.getByRole('button', { name: 'Generate strong password' }));
    expect(passwordField().value.length).toBeGreaterThanOrEqual(32);
    expect(screen.queryByRole('status')).toBeNull();
  });
});
