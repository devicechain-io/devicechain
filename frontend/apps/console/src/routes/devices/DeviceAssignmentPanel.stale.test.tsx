// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useQuery's `data` is sticky across a key change. The assignment target picker must act on
// `current`: offering the previous type's targets while the next type loads would let a fast
// click assign the device to a customer token under the `area` type.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { listCustomers, listAreas } = vi.hoisted(() => ({ listCustomers: vi.fn(), listAreas: vi.fn() }));
vi.mock('@/lib/api/customers', () => ({ listCustomers }));
vi.mock('@/lib/api/areas', () => ({ listAreas }));
vi.mock('@/lib/api/assets', () => ({ listAssets: vi.fn() }));
vi.mock('@/lib/api/relationships', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/relationships')>();
  return { ...actual, listDeviceAssignments: async () => [] };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn() }));

// The real Combobox hides its options behind a popover; this stand-in lists them as buttons.
vi.mock('@/components/ui/combobox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/ui/combobox')>()),
  Combobox: ({
    options,
    onChange,
  }: {
    options: { value: string; label?: string }[];
    onChange: (v: string) => void;
  }) => (
    <div>
      {options.map((o) => (
        <button key={o.value} type="button" onClick={() => onChange(o.value)}>
          {o.label ?? o.value}
        </button>
      ))}
    </div>
  ),
}));

import { DeviceAssignmentPanel } from './DeviceAssignmentPanel';

afterEach(cleanup);
beforeEach(() => {
  listCustomers.mockReset();
  listAreas.mockReset();
});

const page = (token: string, name: string) => ({ results: [{ token, name }] });

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('DeviceAssignmentPanel never offers another type’s targets', () => {
  it('drops the previous type’s targets while the next type loads, then offers the new ones', async () => {
    listCustomers.mockResolvedValue(page('c-1', 'Acme Customer'));
    const next = deferred<ReturnType<typeof page>>();
    listAreas.mockReturnValue(next.promise);

    render(<DeviceAssignmentPanel deviceToken="dev-1" />);
    await screen.findByRole('button', { name: 'Acme Customer' });

    fireEvent.click(screen.getByRole('button', { name: 'Area' }));
    await waitFor(() => expect(listAreas).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole('button', { name: 'Acme Customer' })).toBeNull();

    next.resolve(page('a-1', 'North Area'));
    await screen.findByRole('button', { name: 'North Area' });
    expect(screen.queryByRole('button', { name: 'Acme Customer' })).toBeNull();
  });

  it('drops the previous type’s targets when the next type fails to load', async () => {
    listCustomers.mockResolvedValue(page('c-1', 'Acme Customer'));
    listAreas.mockRejectedValue(new Error('boom'));

    render(<DeviceAssignmentPanel deviceToken="dev-1" />);
    await screen.findByRole('button', { name: 'Acme Customer' });

    fireEvent.click(screen.getByRole('button', { name: 'Area' }));
    await waitFor(() => expect(listAreas).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByRole('button', { name: 'Acme Customer' })).toBeNull();
  });
});
