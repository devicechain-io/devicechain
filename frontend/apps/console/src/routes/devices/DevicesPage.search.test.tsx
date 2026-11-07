// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device list name search and device-type filter. Both are server-side criteria
// (nameContains, deviceType) that ride the URL; clearing drops them. Only the transport is faked.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn() }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'ann', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import DevicesPage from './DevicesPage';

const TYPE = { id: 't1', token: 'pump', name: 'Pump', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null };
const device = (token: string) => ({
  id: token, token, name: `Device ${token}`, description: null, externalId: null, metadata: null,
  createdAt: '2026-10-01T00:00:00Z', enabled: true, disabledAt: null, deviceType: TYPE,
});

let sent: { op: string; variables: any }[] = [];
beforeEach(() => {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: any) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'Devices':
        return Promise.resolve({
          devices: { results: [device('pump-1')], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
        });
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: { results: [TYPE], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
        });
      case 'DeviceTypeByToken':
        return Promise.resolve({ deviceTypesByToken: [TYPE] });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

const renderPage = (search = '') =>
  render(
    <MemoryRouter initialEntries={[`/devices${search}`]}>
      <DevicesPage />
    </MemoryRouter>,
  );
const searches = () => sent.filter((s) => s.op === 'Devices').map((s) => s.variables.criteria);
const last = () => searches()[searches().length - 1];

describe('device search and type filter', () => {
  it('sends the typed name as nameContains after a pause, not per keystroke', async () => {
    renderPage();
    await screen.findByText('Device pump-1');
    const before = searches().length;
    const box = screen.getByLabelText('Search');
    fireEvent.change(box, { target: { value: 'pu' } });
    fireEvent.change(box, { target: { value: 'pump' } });
    expect(searches().length).toBe(before);
    await waitFor(() => expect(last()).toMatchObject({ nameContains: 'pump' }));
    expect(searches().length).toBe(before + 1);
  });

  it('reads the name and type from the URL', async () => {
    renderPage('?q=pump&type=pump');
    await screen.findByText('Device pump-1');
    expect(searches()[0]).toMatchObject({ nameContains: 'pump', deviceType: 'pump' });
    expect((screen.getByLabelText('Search') as HTMLInputElement).value).toBe('pump');
    await waitFor(() => expect(document.getElementById('dev-filter-type')?.textContent).toContain('Pump'));
  });

  it('filters by a device type picked from a server search', async () => {
    renderPage();
    await screen.findByText('Device pump-1');
    fireEvent.click(document.getElementById('dev-filter-type') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Pump', { selector: 'span.block' }));
    await waitFor(() => expect(last()).toMatchObject({ deviceType: 'pump' }));
    // The picker asked the server, not a list it holds.
    expect(sent.some((s) => s.op === 'DeviceTypes' && s.variables.criteria.pageSize === 25)).toBe(true);
  });

  it('clears both filters', async () => {
    renderPage('?q=pump&type=pump');
    await screen.findByText('Device pump-1');
    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
    await waitFor(() => expect(last()).toEqual({ pageNumber: 1, pageSize: 20, deviceType: null }));
    expect((screen.getByLabelText('Search') as HTMLInputElement).value).toBe('');
  });

  it('ignores a blank search', async () => {
    renderPage('?q=%20%20');
    await screen.findByText('Device pump-1');
    expect(searches()[0]).not.toHaveProperty('nameContains');
  });
});
