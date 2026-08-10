// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 Devices are NOT in REGISTRY_RESOURCES — they have their own list and detail
// pages — so `routes/resources.test.tsx` walks straight past them. This file is
// the device's share of that gate, and it has more to prove than the others: a
// device carries an `externalId`, no console form edits it, and updateDevice is a
// full replace. Renaming a device therefore used to erase the handle whatever
// system provisioned it correlates it by. The device kept reporting; it simply
// stopped being findable from the other side, with a success toast on the way out.
//
// The transport is the only seam faked. The form, the type picker and the request
// it builds all run for real, which is the only reason asserting on what went out
// means anything.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DeviceForm } from './DeviceForm';
import type { Device } from '@/lib/api/device-management';

const METADATA = '{"fitted":"2026-03-04","installer":"crew-7"}';

const DEVICE_TYPE = {
  id: 'dt-1',
  token: 'gateway',
  name: 'Gateway',
  icon: null,
  backgroundColor: null,
  foregroundColor: null,
  borderColor: null,
};

function device(overrides: Partial<Device> = {}): Device {
  return {
    id: 'd-1',
    token: 'sensor-14',
    name: 'Sensor 14',
    description: 'By the loading dock.',
    externalId: 'ERP-99213',
    metadata: METADATA,
    createdAt: '2026-08-01T00:00:00Z',
    deviceType: DEVICE_TYPE,
    ...overrides,
  } as Device;
}

/** The mutation requests actually sent — the type picker's query is not one. */
function requests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((call) => call[0] === 'device-management')
    .map((call) => (call[2] as { request?: Record<string, unknown> } | undefined)?.request)
    .filter((r): r is Record<string, unknown> => r != null);
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    return Promise.resolve({
      // The type picker. An EMPTY list disables the save outright, so this has to
      // answer or every assertion below would be about a button that never fired.
      deviceTypes: {
        results: [DEVICE_TYPE],
        pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
      },
      devicesByToken: [],
      updateDevice: {},
      createDevice: {},
    });
  });
});

async function saveEdit(entity: Device) {
  render(<DeviceForm device={entity} onDone={vi.fn()} />);
  const save = await screen.findByRole('button', { name: 'Save changes' });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  await waitFor(() => expect(requests()).toHaveLength(1));
  return requests()[0];
}

describe('editing a device', () => {
  it('carries the externalId and metadata it does not edit', async () => {
    const request = await saveEdit(device());

    expect(request.externalId).toBe('ERP-99213');
    expect(request.metadata).toBe(METADATA);
    // …and it is genuinely an edit of this device rather than a stale record sent
    // back whole: the token and the type it resolves through are the entity's.
    expect(request.token).toBe('sensor-14');
    expect(request.deviceTypeToken).toBe('gateway');
  });

  // The counterweight. Without it the assertions above would pass equally well
  // against a form that had started sending some other device's fields, or that
  // invented an externalId for a device that has none.
  it('sends null for a device that never had one', async () => {
    const request = await saveEdit(device({ externalId: null, metadata: null }));

    expect(request.externalId).toBeNull();
    expect(request.metadata).toBeNull();
  });

  // The edited fields still have to arrive, or "nothing was destroyed" would be
  // satisfied by a form that saved nothing at all.
  it('sends the name the operator typed', async () => {
    render(<DeviceForm device={device()} onDone={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Dock sensor' } });
    const save = screen.getByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);

    await waitFor(() => expect(requests()).toHaveLength(1));
    expect(requests()[0].name).toBe('Dock sensor');
    expect(requests()[0].externalId).toBe('ERP-99213');
  });
});
