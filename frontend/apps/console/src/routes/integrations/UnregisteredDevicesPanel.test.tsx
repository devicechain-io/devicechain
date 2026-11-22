// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The unregistered-devices panel on its own: loading, empty, failed and populated states, the
// create-a-device link each row carries (with the device type chosen only when the scope is a
// single type), what a member without device:write does not get, and clearing the list.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn(), toast: vi.fn(), confirm: vi.fn() }));

vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => h.confirm }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { UnregisteredDevicesPanel } from './UnregisteredDevicesPanel';

const ROWS = [
  { id: 'u1', integration: 'eu868-gw', scheme: 'DEV_EUI', value: '0102030405060708', firstSeen: '2026-10-01T00:00:00Z', lastSeen: '2026-10-02T00:00:00Z', count: 7 },
  { id: 'u2', integration: 'eu868-gw', scheme: 'custom.serial', value: 'SN-42', firstSeen: '2026-10-01T00:00:00Z', lastSeen: '2026-10-03T00:00:00Z', count: 1 },
];

const ONE_TYPE = { token: 'eu868-gw', deviceTypes: [{ id: 'dt-1', token: 'meter', name: 'Meter' }] };
const TWO_TYPES = {
  token: 'eu868-gw',
  deviceTypes: [
    { id: 'dt-1', token: 'meter', name: 'Meter' },
    { id: 'dt-2', token: 'pump', name: 'Pump' },
  ],
};

let rows = ROWS;
let listError: Error | null = null;
let clearError: Error | null = null;
let listCalls: Record<string, unknown>[] = [];

beforeEach(() => {
  rows = ROWS;
  listError = null;
  clearError = null;
  listCalls = [];
  h.toast.mockReset();
  h.confirm.mockReset();
  h.confirm.mockResolvedValue(true);
  h.gql.mockReset();
  h.gql.mockImplementation((_service: string, doc: unknown, variables: Record<string, any>) => {
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    if (op === 'UnregisteredIntegrationDevices') {
      listCalls.push(variables.criteria);
      if (listError) return Promise.reject(listError);
      return Promise.resolve({
        unregisteredIntegrationDevices: {
          results: rows,
          pagination: { pageStart: 1, pageEnd: rows.length, totalRecords: rows.length },
        },
      });
    }
    if (op === 'ClearUnregisteredIntegrationDevices') {
      if (clearError) return Promise.reject(clearError);
      const n = rows.length;
      rows = [];
      return Promise.resolve({ clearUnregisteredIntegrationDevices: n });
    }
    return Promise.resolve({});
  });
});
afterEach(cleanup);

function renderPanel(integration = ONE_TYPE, canWrite = true) {
  return render(
    <MemoryRouter>
      <UnregisteredDevicesPanel integration={integration} canWrite={canWrite} />
    </MemoryRouter>,
  );
}

describe('UnregisteredDevicesPanel', () => {
  it('shows a loading state, then the identifiers with their scheme, count and a create link', async () => {
    renderPanel();
    expect(screen.getByText('Loading unregistered devices…')).toBeTruthy();
    expect(await screen.findByText('0102030405060708')).toBeTruthy();
    expect(screen.getByText('DEV_EUI')).toBeTruthy();
    expect(screen.getByText('custom.serial')).toBeTruthy();
    expect(screen.getByText('7')).toBeTruthy();
    expect(listCalls[0]).toEqual({ integration: 'eu868-gw', pageNumber: 1, pageSize: 50 });
    expect(screen.getAllByRole('link', { name: 'Create a device with this alias' })).toHaveLength(2);
  });

  it('chooses the device type in the link only when the scope is a single type', async () => {
    renderPanel(ONE_TYPE);
    const [first] = await screen.findAllByRole('link', { name: 'Create a device with this alias' });
    const one = new URL(first.getAttribute('href')!, 'http://x');
    expect(one.pathname).toBe('/devices');
    expect(one.searchParams.get('newAlias')).toBe('DEV_EUI|0102030405060708');
    expect(one.searchParams.get('newType')).toBe('meter');
    cleanup();

    renderPanel(TWO_TYPES);
    const [again] = await screen.findAllByRole('link', { name: 'Create a device with this alias' });
    const two = new URL(again.getAttribute('href')!, 'http://x');
    expect(two.searchParams.get('newAlias')).toBe('DEV_EUI|0102030405060708');
    expect(two.searchParams.has('newType')).toBe(false);
  });

  it('says so when nothing has been seen, and offers nothing to clear', async () => {
    rows = [];
    renderPanel();
    expect(await screen.findByText(/None seen\./)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Clear list' })).toBeNull();
  });

  it('shows the read error when the first load fails', async () => {
    listError = new Error('the read failed');
    renderPanel();
    expect(await screen.findByText('Failed to load data.')).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('hides the create links and the clear button from a member who cannot write', async () => {
    renderPanel(ONE_TYPE, false);
    expect(await screen.findByText('0102030405060708')).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Create a device with this alias' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Clear list' })).toBeNull();
  });

  it('clears the list after confirming, reports how many went, and reloads from the first page', async () => {
    renderPanel();
    await screen.findByText('0102030405060708');
    fireEvent.click(screen.getByRole('button', { name: 'Clear list' }));
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('List cleared (2 removed)'));
    expect(h.confirm).toHaveBeenCalledTimes(1);
    expect(await screen.findByText(/None seen\./)).toBeTruthy();
    const cleared = h.gql.mock.calls.filter((c) => /ClearUnregisteredIntegrationDevices/.test(String(c[1])));
    expect(cleared.map((c) => c[2])).toEqual([{ integration: 'eu868-gw' }]);
  });

  it('does nothing when the confirmation is declined', async () => {
    h.confirm.mockResolvedValue(false);
    renderPanel();
    await screen.findByText('0102030405060708');
    fireEvent.click(screen.getByRole('button', { name: 'Clear list' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalled());
    expect(h.gql.mock.calls.some((c) => /ClearUnregisteredIntegrationDevices/.test(String(c[1])))).toBe(false);
    expect(screen.getByText('0102030405060708')).toBeTruthy();
  });

  it('shows a refused clear as an error toast and keeps the rows', async () => {
    clearError = new Error('not allowed');
    renderPanel();
    await screen.findByText('0102030405060708');
    fireEvent.click(screen.getByRole('button', { name: 'Clear list' }));
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('not allowed', 'error'));
    expect(screen.getByText('0102030405060708')).toBeTruthy();
  });
});
