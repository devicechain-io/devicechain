// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "expected provisioning profile" field of the device create form: optional, offered only
// for the profiles that fit the chosen type and only to members who may list profiles, sent only
// when chosen, and never part of an edit. Only the transport is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'boss', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DeviceForm } from './DeviceForm';
import type { Device } from '@/lib/api/device-management';

const TYPE = { id: 'dt-1', token: 'gateway', name: 'Gateway', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null };
const PROFILE = {
  id: 'p-1',
  token: 'acme-fleet',
  name: 'Acme fleet',
  description: null,
  provisionKey: 'k',
  strategy: 'ALLOW_NEW',
  credentialType: 'ACCESS_TOKEN',
  enabled: true,
  expiresAt: null,
  maxDevices: null,
  provisionedDeviceCount: 0,
  secretIssued: true,
  createdAt: null,
  updatedAt: null,
  deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
};

let sent: { op: string; variables: any }[] = [];

beforeEach(() => {
  sent = [];
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, doc: unknown, variables: any) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'DeviceTypes':
        return Promise.resolve({ deviceTypes: { results: [TYPE], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } } });
      case 'ProvisioningProfiles':
        return Promise.resolve({
          provisioningProfiles: { results: [PROFILE], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
        });
      case 'DeviceByToken':
        return Promise.resolve({ devicesByToken: [] });
      default:
        return Promise.resolve({ createDevice: {} });
    }
  });
});
afterEach(cleanup);

const creates = () => sent.filter((s) => s.op === 'CreateDevice');

async function fillDevice() {
  fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'pump-7' } });
  fireEvent.click(document.getElementById('r-type') as HTMLButtonElement);
  fireEvent.click(await screen.findByText('Gateway'));
}

async function save() {
  const button = screen.getByRole('button', { name: 'Create device' });
  await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(button);
  await waitFor(() => expect(creates()).toHaveLength(1));
  return creates()[0].variables.request as Record<string, unknown>;
}

describe('the expected provisioning profile on device create', () => {
  it('sends the chosen profile as the earmark, offering only the profiles of the chosen type', async () => {
    render(<DeviceForm offerEarmark onDone={vi.fn()} />);
    await fillDevice();
    await waitFor(() =>
      expect(sent.some((s) => s.op === 'ProvisioningProfiles' && s.variables.criteria.deviceType === 'gateway')).toBe(true),
    );

    fireEvent.click(document.getElementById('dev-earmark') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Acme fleet'));

    const request = await save();
    expect(request.provisioningProfileToken).toBe('acme-fleet');
    expect(request.deviceTypeToken).toBe('gateway');
  });

  it('sends no earmark at all when none is chosen', async () => {
    render(<DeviceForm offerEarmark onDone={vi.fn()} />);
    await fillDevice();
    const request = await save();
    expect(request).not.toHaveProperty('provisioningProfileToken');
  });

  it('is not offered, and no profile is listed, when the caller may not use it', async () => {
    render(<DeviceForm onDone={vi.fn()} />);
    await fillDevice();
    expect(document.getElementById('dev-earmark')).toBeNull();
    const request = await save();
    expect(request).not.toHaveProperty('provisioningProfileToken');
    expect(sent.some((s) => s.op === 'ProvisioningProfiles')).toBe(false);
  });

  it('is not part of an edit', async () => {
    const device = {
      id: 'd-1',
      token: 'sensor-14',
      name: 'Sensor 14',
      description: null,
      externalId: 'ERP-1',
      metadata: '{"a":1}',
      createdAt: '2026-08-01T00:00:00Z',
      deviceType: TYPE,
    } as Device;
    render(<DeviceForm device={device} offerEarmark onDone={vi.fn()} />);
    await screen.findByLabelText('Name');
    expect(document.getElementById('dev-earmark')).toBeNull();
    const button = await screen.findByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(button);
    await waitFor(() => expect(sent.some((s) => s.op === 'UpdateDevice')).toBe(true));
    const update = sent.find((s) => s.op === 'UpdateDevice')!.variables.request as Record<string, unknown>;
    expect(update).not.toHaveProperty('provisioningProfileToken');
  });
});
