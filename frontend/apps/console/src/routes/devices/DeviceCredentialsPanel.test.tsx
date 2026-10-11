// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. Only the GraphQL transport, the toast
// and the confirm dialog are faked; the panel, its request shaping and the API module
// all run.
//
// 🔴 WHY THIS FILE EXISTS. The platform keeps only a keyed digest of an MQTT_BASIC
// password, so the moment it is set is the ONLY moment the operator can read it. A
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
  credentialId: 'bearer-abc',
  credentialValue: 'LEAKED-TOKEN-VALUE',
};

function route(rows: unknown[]) {
  gqlMock.mockImplementation((_s: string, doc: string) => {
    const d = String(doc);
    if (d.includes('mutation CreateDeviceCredential')) return Promise.resolve({ createDeviceCredential: {} });
    if (d.includes('mutation UpdateDeviceCredential')) return Promise.resolve({ updateDeviceCredential: {} });
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

  it('keeps access tokens as readable identifiers with no password UI', async () => {
    route([tokenRow]);
    render(<DeviceCredentialsPanel deviceToken="dev-1" />);
    expect(await screen.findByText('bearer-abc')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy id' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /set new password/i })).toBeNull();
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
