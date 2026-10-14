// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the device-type form says about its maintenance window. UpdateDeviceType is a PARTIAL
// update, so "unchanged" must be spelled by leaving `maintenanceWindowToken` out: sending
// null there DETACHES the window, with a success toast and no other sign.
//
// Only the GraphQL transport is faked; the form, the picker and the request it builds are real.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The token field reads the tenant's token masks through the auth context; neither matters here.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: true, isIdentityAuthenticated: false, claims: { authorities: ['device:write'] } }),
}));
vi.mock('@/lib/token-masks', () => ({ getTokenMasks: () => Promise.resolve({}) }));

import { DeviceTypeForm } from './DeviceTypeForm';
import type { DeviceType } from '@/lib/api/device-management';

const WINDOWS = [
  { id: 'w1', token: 'night', name: 'Night', description: null, timezone: 'Europe/Berlin', slots: '[]', metadata: null, createdAt: null, updatedAt: null },
  { id: 'w2', token: 'weekend', name: null, description: null, timezone: 'UTC', slots: '[]', metadata: null, createdAt: null, updatedAt: null },
];

function deviceType(window: DeviceType['maintenanceWindow']): DeviceType {
  return {
    id: 'dt-1',
    token: 'thermostat',
    name: 'Thermostat',
    description: null,
    metadata: null,
    createdAt: '2026-08-01T00:00:00Z',
    maintenanceWindow: window,
  } as DeviceType;
}

type Write = { token?: unknown; request: Record<string, unknown> };
function writes(): Write[] {
  const out: Write[] = [];
  for (const call of gqlMock.mock.calls) {
    const vars = call[2] as { request?: Record<string, unknown>; token?: unknown } | undefined;
    if (vars?.request == null) continue;
    out.push({ token: vars.token, request: vars.request });
  }
  return out;
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation((_svc: string, doc: unknown) => {
    if (String(doc).includes('query MaintenanceWindows')) {
      return Promise.resolve({
        maintenanceWindows: { results: WINDOWS, pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 } },
      });
    }
    return Promise.resolve({ updateDeviceType: { id: 'dt-1', token: 'thermostat' }, createDeviceType: { id: 'dt-1', token: 'thermostat' } });
  });
});

function renderForm(entity?: DeviceType) {
  return render(
    <MemoryRouter>
      <DeviceTypeForm entity={entity} onDone={vi.fn()} />
    </MemoryRouter>,
  );
}

async function save() {
  fireEvent.click(await screen.findByRole('button', { name: /Save changes|Create device type/ }));
  await waitFor(() => expect(writes().length).toBeGreaterThan(0));
  return writes()[writes().length - 1];
}

async function choose(token: string) {
  const trigger = document.getElementById('dt-window')!;
  await waitFor(() => expect(trigger.hasAttribute('disabled')).toBe(false));
  fireEvent.click(trigger);
  fireEvent.click(await waitFor(() => {
    const option = document.querySelector(`[data-shot-value="${token}"]`);
    expect(option).not.toBeNull();
    return option!;
  }));
}

describe('DeviceTypeForm maintenance window', () => {
  it('does not mention the window when it was left alone', async () => {
    renderForm(deviceType({ token: 'night', name: 'Night', timezone: 'Europe/Berlin' }));
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Renamed' } });
    const sent = await save();
    expect(sent.token).toBe('thermostat');
    expect(sent.request.name).toBe('Renamed');
    expect('maintenanceWindowToken' in sent.request).toBe(false);
  });

  it('does not mention it for a type that never had one', async () => {
    renderForm(deviceType(null));
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Renamed' } });
    expect('maintenanceWindowToken' in (await save()).request).toBe(false);
  });

  it('sends the token when a different window is chosen', async () => {
    renderForm(deviceType({ token: 'night', name: 'Night', timezone: 'Europe/Berlin' }));
    await choose('weekend');
    expect((await save()).request.maintenanceWindowToken).toBe('weekend');
  });

  it('detaches with an explicit null only when the operator clears it', async () => {
    renderForm(deviceType({ token: 'night', name: 'Night', timezone: 'Europe/Berlin' }));
    fireEvent.click(await screen.findByRole('button', { name: /clear/i }));
    const sent = await save();
    expect('maintenanceWindowToken' in sent.request).toBe(true);
    expect(sent.request.maintenanceWindowToken).toBeNull();
  });

  it('names the window on a create only when one is chosen', async () => {
    renderForm();
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Thermostat' } });
    fireEvent.change(document.getElementById('r-token')!, { target: { value: 'thermostat' } });
    expect('maintenanceWindowToken' in (await save()).request).toBe(false);
  });
});
