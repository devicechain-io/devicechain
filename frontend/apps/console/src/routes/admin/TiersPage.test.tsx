// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tier list page: loading, empty and error states, and a populated row that opens the
// tier. A failed first read shows the error rather than an empty list.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock('@/lib/api/admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/admin')>()),
  listTenantTierCatalog: (...a: unknown[]) => h.list(...a),
}));

import TiersPage from './TiersPage';

const t = (key: string) => i18n.t(key);

function row() {
  return { token: 'gold', name: 'Gold', description: 'Everything on', color: null, tenantCount: 3 };
}

describe('TiersPage', () => {
  beforeEach(() => {
    h.list.mockReset();
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<TiersPage />);
    expect(screen.getByRole('status').textContent).toBe(t('tiers:loading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when nothing exists', async () => {
    h.list.mockResolvedValue([]);
    renderListPage(<TiersPage />);
    expect(await screen.findByText(t('tiers:empty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<TiersPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('tiers:empty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a row that opens its detail route', async () => {
    h.list.mockResolvedValue([row()]);
    renderListPage(<TiersPage />);
    const rowEl = await screen.findByRole('button', { name: /Everything on/ });
    expect(screen.getByRole('table')).toBeTruthy();
    fireEvent.click(rowEl);
    expect(screen.getByTestId('where').textContent).toBe('/admin/tiers/gold');
  });
});
