// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alias field of the device form: offered only to members who may see aliases, sent on create
// as typed, validated per scheme, prefilled from an unregistered sighting, and on edit sent ONLY
// when the operator changed it (an update REPLACES the stored list, so an unrelated save must not
// name it). Only the transport is faked.

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
import { rowOf } from './deviceAliases';
import type { Device } from '@/lib/api/device-management';

const TYPE = { id: 'dt-1', token: 'meter', name: 'Meter', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null };

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
      case 'DeviceByToken':
        return Promise.resolve({ devicesByToken: [] });
      default:
        return Promise.resolve({ createDevice: {}, updateDevice: {} });
    }
  });
});
afterEach(cleanup);

const requestOf = (op: string) => sent.find((s) => s.op === op)?.variables.request as Record<string, unknown> | undefined;

const device = (aliases: { scheme: string; value: string }[] | null): Device & { aliases: typeof aliases } =>
  ({
    id: 'd-1',
    token: 'meter-7',
    name: 'Meter 7',
    description: null,
    externalId: null,
    metadata: null,
    createdAt: '2026-08-01T00:00:00Z',
    deviceType: TYPE,
    aliases,
  }) as Device & { aliases: typeof aliases };

async function save(name: string) {
  const button = await screen.findByRole('button', { name });
  await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(button);
}

describe('creating a device with an alias', () => {
  async function fillToken() {
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'meter-7' } });
    fireEvent.click(document.getElementById('r-type') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Meter'));
  }

  it('sends the typed alias with its scheme', async () => {
    render(<DeviceForm offerAliases onDone={vi.fn()} />);
    await fillToken();
    fireEvent.click(screen.getByRole('button', { name: 'Add alias' }));
    fireEvent.change(screen.getByLabelText('Alias value'), { target: { value: '70b3d57ed0012345' } });
    await save('Create device');
    await waitFor(() => expect(requestOf('CreateDevice')).toBeDefined());
    expect(requestOf('CreateDevice')!.aliases).toEqual([{ scheme: 'lorawan.deveui', value: '70b3d57ed0012345' }]);
  });

  it('sends no aliases at all when none is entered, or when the field is not offered', async () => {
    render(<DeviceForm offerAliases onDone={vi.fn()} />);
    await fillToken();
    await save('Create device');
    await waitFor(() => expect(requestOf('CreateDevice')).toBeDefined());
    expect(requestOf('CreateDevice')).not.toHaveProperty('aliases');

    cleanup();
    sent = [];
    render(<DeviceForm onDone={vi.fn()} />);
    await fillToken();
    expect(screen.queryByText('Aliases')).toBeNull();
    await save('Create device');
    await waitFor(() => expect(requestOf('CreateDevice')).toBeDefined());
    expect(requestOf('CreateDevice')).not.toHaveProperty('aliases');
  });

  it('refuses a malformed DevEUI before sending, saying what a DevEUI is', async () => {
    render(<DeviceForm offerAliases onDone={vi.fn()} />);
    await fillToken();
    fireEvent.click(screen.getByRole('button', { name: 'Add alias' }));
    fireEvent.change(screen.getByLabelText('Alias value'), { target: { value: '70B3D5' } });
    expect(await screen.findByText('A DevEUI is exactly 16 hexadecimal digits.')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Create device' }) as HTMLButtonElement).disabled).toBe(true);
    expect(requestOf('CreateDevice')).toBeUndefined();
  });

  it('starts from an alias an integration saw, and the type it can resolve', async () => {
    render(<DeviceForm offerAliases initialAliases={[rowOf('lorawan.deveui', '70B3D57ED0012345')]} initialTypeToken="meter" onDone={vi.fn()} />);
    expect((await screen.findByLabelText('Alias value')) as HTMLInputElement).toHaveProperty('value', '70B3D57ED0012345');
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'meter-7' } });
    await save('Create device');
    await waitFor(() => expect(requestOf('CreateDevice')).toBeDefined());
    expect(requestOf('CreateDevice')).toMatchObject({
      deviceTypeToken: 'meter',
      aliases: [{ scheme: 'lorawan.deveui', value: '70B3D57ED0012345' }],
    });
  });

  it('takes a custom scheme by name', async () => {
    render(<DeviceForm offerAliases onDone={vi.fn()} />);
    await fillToken();
    fireEvent.click(screen.getByRole('button', { name: 'Add alias' }));
    fireEvent.click(document.getElementById('device-alias-kind-0') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Custom scheme'));
    fireEvent.change(screen.getByLabelText('Scheme name'), { target: { value: 'serial' } });
    fireEvent.change(screen.getByLabelText('Alias value'), { target: { value: 'SN-0042' } });
    await save('Create device');
    await waitFor(() => expect(requestOf('CreateDevice')).toBeDefined());
    expect(requestOf('CreateDevice')!.aliases).toEqual([{ scheme: 'custom.serial', value: 'SN-0042' }]);
  });
});

describe('editing a device with aliases', () => {
  it('shows the stored aliases', async () => {
    render(<DeviceForm device={device([{ scheme: 'lorawan.deveui', value: '70B3D57ED0012345' }])} offerAliases onDone={vi.fn()} />);
    expect(((await screen.findByLabelText('Alias value')) as HTMLInputElement).value).toBe('70B3D57ED0012345');
  });

  it('does not name the aliases when only the name changed: the update would replace the list', async () => {
    render(<DeviceForm device={device([{ scheme: 'lorawan.deveui', value: '70B3D57ED0012345' }])} offerAliases onDone={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Renamed' } });
    await save('Save changes');
    await waitFor(() => expect(requestOf('UpdateDevice')).toBeDefined());
    expect(requestOf('UpdateDevice')).not.toHaveProperty('aliases');
  });

  it('sends the whole new list when an alias was changed, and an empty list when the last was removed', async () => {
    render(<DeviceForm device={device([{ scheme: 'lorawan.deveui', value: '70B3D57ED0012345' }])} offerAliases onDone={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Alias value'), { target: { value: '70B3D57ED0012346' } });
    await save('Save changes');
    await waitFor(() => expect(requestOf('UpdateDevice')).toBeDefined());
    expect(requestOf('UpdateDevice')!.aliases).toEqual([{ scheme: 'lorawan.deveui', value: '70B3D57ED0012346' }]);

    cleanup();
    sent = [];
    render(<DeviceForm device={device([{ scheme: 'lorawan.deveui', value: '70B3D57ED0012345' }])} offerAliases onDone={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Remove alias' }));
    await save('Save changes');
    await waitFor(() => expect(requestOf('UpdateDevice')).toBeDefined());
    expect(requestOf('UpdateDevice')!.aliases).toEqual([]);
  });

  it('is not offered when the device came back with no aliases field (a member limited to part of the tenant)', async () => {
    render(<DeviceForm device={device(null)} offerAliases onDone={vi.fn()} />);
    await screen.findByLabelText('Name');
    expect(screen.queryByText('Aliases')).toBeNull();
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Renamed' } });
    await save('Save changes');
    await waitFor(() => expect(requestOf('UpdateDevice')).toBeDefined());
    expect(requestOf('UpdateDevice')).not.toHaveProperty('aliases');
  });
});
