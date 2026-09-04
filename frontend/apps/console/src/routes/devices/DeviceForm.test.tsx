// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 Devices are NOT in REGISTRY_RESOURCES — they have their own list and detail
// pages — so `routes/resources.test.tsx` walks straight past them. This file is the
// device's share of that gate, and it has more to prove than the others: a device
// carries an `externalId`, and no console form edits it.
//
// 🔴 THE ASSERTIONS HERE ARE THE INVERSE OF WHAT THEY WERE, because the contract
// under them inverted. updateDevice used to be a FULL REPLACE: the stored record was
// rebuilt from the request, so renaming a device erased the handle whatever system
// provisioned it correlates it by — the device kept reporting, it simply stopped
// being findable from the other side, with a success toast on the way out. The form
// worked around that by re-sending every field, and this file checked that it did.
//
// updateDevice is now a PARTIAL update, and there the carry-forward is the bug
// rather than the fix: a form re-sending fields it never showed is writing them back
// from a snapshot it read when the page loaded, so two operators on two tabs each
// silently overwrite the other. So what this file now checks is that the request
// carries ONLY what the form edits, and that the fields it does not edit survive by
// being ABSENT. "Sent as null" is the failure mode to watch for — that is not
// "leave it alone", it is "clear the column".
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

/**
 * The mutation writes actually sent — the type picker's query is not one. The token
 * is captured alongside the request rather than from inside it: a partial update
 * names its subject in the mutation's own argument, which is what makes moving a
 * device's token unrepresentable rather than merely refused.
 */
type Write = { request: Record<string, unknown>; token: unknown };

function writes(): Write[] {
  const out: Write[] = [];
  for (const call of gqlMock.mock.calls) {
    if (call[0] !== 'device-management') continue;
    const vars = call[2] as { request?: Record<string, unknown>; token?: unknown } | undefined;
    if (vars?.request == null) continue;
    out.push({ request: vars.request, token: vars.token });
  }
  return out;
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

async function saveEdit(entity: Device): Promise<Write> {
  render(<DeviceForm device={entity} onDone={vi.fn()} />);
  const save = await screen.findByRole('button', { name: 'Save changes' });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  await waitFor(() => expect(writes()).toHaveLength(1));
  return writes()[0];
}

// Exactly what the form edits, and nothing else. Keyed by name rather than counted,
// so a field that starts being sent is named in the failure.
const EDITED = ['name', 'description', 'deviceTypeToken'];

describe('editing a device', () => {
  it('omits the externalId and metadata it does not edit', async () => {
    const { request, token } = await saveEdit(device());

    // 🔴 `not.toHaveProperty`, not `toBeUndefined`. Under a partial update an
    // explicit null CLEARS the column, and `{externalId: null}` would satisfy
    // "undefined-ish" checks while being the very erasure this guards against.
    expect(request).not.toHaveProperty('externalId');
    expect(request).not.toHaveProperty('metadata');
    // The token left the input entirely and now names the subject alongside it.
    expect(request).not.toHaveProperty('token');
    expect(token).toBe('sensor-14');
    expect(request.deviceTypeToken).toBe('gateway');
  });

  // The whole property, stated once: a field this form does not edit must not be
  // in the request at all. Stronger than naming externalId and metadata by hand,
  // because it also covers whatever is added to the request type later.
  it('sends nothing beyond the fields it edits', async () => {
    const { request } = await saveEdit(device());
    expect(Object.keys(request).sort()).toEqual([...EDITED].sort());
  });

  // The counterweight. Without it, "sends nothing it does not edit" would be
  // satisfied equally well by a form that saved nothing at all.
  it('sends the name the operator typed', async () => {
    render(<DeviceForm device={device()} onDone={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Dock sensor' } });
    const save = screen.getByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);

    await waitFor(() => expect(writes()).toHaveLength(1));
    const { request } = writes()[0];
    expect(request.name).toBe('Dock sensor');
    // …and typing a name did not start sending the externalId along with it.
    expect(request).not.toHaveProperty('externalId');
  });

  // A device that has neither is indistinguishable from one that has both, as far
  // as this request is concerned — because neither is mentioned either way. That is
  // the point: absence is not "null", and the form has no business deciding.
  it('treats a device with no externalId identically', async () => {
    const { request } = await saveEdit(device({ externalId: null, metadata: null }));
    expect(Object.keys(request).sort()).toEqual([...EDITED].sort());
  });
});
