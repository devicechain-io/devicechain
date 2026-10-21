// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport, the toast and
// the session are faked. What this pins is the panel's one decision — whether to offer a
// command at all — and that the command it offers is built from the device's credential
// and its PUBLISHED metrics. The command text itself is pinned by connectSnippet.test.ts
// and judged by the platform's own ingest tests.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { tenant: 'acme' } }) }));

import { DeviceConnectPanel } from './DeviceConnectPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

const tokenRow = {
  id: '1',
  token: 'cred-token',
  credentialType: 'ACCESS_TOKEN',
  credentialId: 'bearer-abc',
  enabled: true,
  expiresAt: null,
  createdAt: '2026-01-01T00:00:00Z',
};
const mqttRow = { ...tokenRow, id: '2', token: 'cred-mqtt', credentialType: 'MQTT_BASIC', credentialId: 'sensor-user' };

function route(rows: unknown[], metrics: unknown[] | null = []) {
  gqlMock.mockImplementation((_s: string, doc: string) => {
    const d = String(doc);
    if (d.includes('query DeviceMetricVocabulary')) {
      return Promise.resolve({ deviceMetricVocabulary: metrics === null ? null : { metrics } });
    }
    return Promise.resolve({ deviceCredentials: { results: rows, pagination: { totalRecords: rows.length } } });
  });
}

const command = () => screen.getByTestId('connect-command').textContent ?? '';

describe('DeviceConnectPanel', () => {
  it('offers no command for a disabled device', async () => {
    route([tokenRow]);
    render(<DeviceConnectPanel deviceToken="dev-1" deviceEnabled={false} />);
    expect(await screen.findByText('This device is disabled')).toBeTruthy();
    expect(screen.queryByTestId('connect-command')).toBeNull();
  });

  it('offers no command without a usable credential', async () => {
    route([{ ...tokenRow, enabled: false }, { ...mqttRow, expiresAt: '2000-01-01T00:00:00Z' }]);
    render(<DeviceConnectPanel deviceToken="dev-1" deviceEnabled />);
    expect(await screen.findByText('Add a credential first')).toBeTruthy();
    expect(screen.queryByTestId('connect-command')).toBeNull();
  });

  it('builds the command from the credential and the published metrics', async () => {
    route([tokenRow], [
      { metricKey: 'pressure', dataType: 'DOUBLE', unit: null, minValue: 100, maxValue: 200, allowedValues: null, calculated: false },
      // A device-sent value under a calculated key is dropped, so the example omits it.
      { metricKey: 'pressure_kpa', dataType: 'DOUBLE', unit: null, minValue: 1, maxValue: 2, allowedValues: null, calculated: true },
    ]);
    render(<DeviceConnectPanel deviceToken="dev-1" deviceEnabled />);
    await screen.findByTestId('connect-command');
    expect(command()).not.toContain('pressure_kpa');
    expect(command()).toContain(`-u 'acme:bearer-abc'`);
    expect(command()).toContain(`-t '<instance-id>/acme/devices/dev-1/events'`);
    expect(command()).toContain('"pressure":"100"');
    expect(command()).not.toContain(' -P ');
    expect(screen.getByText(/published version, with values it accepts\.$/)).toBeTruthy();
    // The quoting is POSIX; pasted into PowerShell or cmd.exe it sends something else.
    expect(screen.getByText(/written for a POSIX shell/)).toBeTruthy();

    fireEvent.click(screen.getByRole('radio', { name: 'HTTP' }));
    await waitFor(() => expect(command()).toContain(`curl -i -X POST 'http://<ingest-host>:8081/<instance-id>/acme/events'`));
  });

  it('writes a placeholder, never a password, for MQTT_BASIC', async () => {
    route([mqttRow]);
    render(<DeviceConnectPanel deviceToken="dev-1" deviceEnabled />);
    await screen.findByTestId('connect-command');
    expect(command()).toContain(`-P '<password>'`);
    // The password authenticates the CONNECT only: an MQTT event body that carries a
    // credential is refused by the platform broker.
    expect(command()).not.toContain('credentialSecret');
    expect(command()).not.toContain('credentialId');
    expect(screen.getByText(/shown once, when it was set/)).toBeTruthy();
    // No published metric to use: the fallback is named as such.
    expect(screen.getByText(/sends an undeclared reading/)).toBeTruthy();
  });

  it('copies the command shown', async () => {
    route([tokenRow]);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    render(<DeviceConnectPanel deviceToken="dev-1" deviceEnabled />);
    await screen.findByTestId('connect-command');
    fireEvent.click(screen.getByRole('button', { name: 'Copy command' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(command()));
  });
});
