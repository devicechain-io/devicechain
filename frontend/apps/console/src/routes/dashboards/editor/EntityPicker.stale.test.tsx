// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useQuery's `data` is sticky: it keeps the previous kind's rows while the next kind loads
// and after it fails. The picker must act on `current`, or a fast click would bind a
// customer token as an `area`.
import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { listCustomers, listAreas } = vi.hoisted(() => ({ listCustomers: vi.fn(), listAreas: vi.fn() }));
vi.mock('@/lib/api/customers', () => ({ listCustomers }));
vi.mock('@/lib/api/areas', () => ({ listAreas }));
vi.mock('@/lib/api/assets', () => ({ listAssets: vi.fn() }));
vi.mock('@/lib/api/device-management', () => ({ listDevices: vi.fn() }));

// The real Combobox hides its options behind a popover; this stand-in lists them.
vi.mock('@/components/ui/combobox', () => ({
  Combobox: ({ options }: { options: { value: string; label?: string }[] }) => (
    <ul>
      {options.map((o) => (
        <li key={o.value}>{o.label ?? o.value}</li>
      ))}
    </ul>
  ),
}));

import { EntityPicker } from './EntityPicker';

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

describe('EntityPicker never offers another kind’s entities', () => {
  it('drops the previous kind while the next kind loads, then offers the new one', async () => {
    listCustomers.mockResolvedValue(page('c-1', 'Acme Customer'));
    const next = deferred<ReturnType<typeof page>>();
    listAreas.mockReturnValue(next.promise);

    const { rerender } = render(<EntityPicker kind="customer" value="" onChange={vi.fn()} />);
    await screen.findByText('Acme Customer');

    rerender(<EntityPicker kind="area" value="" onChange={vi.fn()} />);
    await waitFor(() => expect(listAreas).toHaveBeenCalledTimes(1));
    expect(screen.queryByText('Acme Customer')).toBeNull();

    next.resolve(page('a-1', 'North Area'));
    await screen.findByText('North Area');
    expect(screen.queryByText('Acme Customer')).toBeNull();
  });

  it('drops the previous kind when the next kind fails to load', async () => {
    listCustomers.mockResolvedValue(page('c-1', 'Acme Customer'));
    listAreas.mockRejectedValue(new Error('boom'));

    const { rerender } = render(<EntityPicker kind="customer" value="" onChange={vi.fn()} />);
    await screen.findByText('Acme Customer');

    rerender(<EntityPicker kind="area" value="" onChange={vi.fn()} />);
    await waitFor(() => expect(listAreas).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByText('Acme Customer')).toBeNull();
  });
});
