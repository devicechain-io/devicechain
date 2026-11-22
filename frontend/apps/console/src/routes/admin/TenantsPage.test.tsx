// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant list page: its loading, empty and error states, and a populated row that
// opens the tenant. A failed read must show the failure, never an empty table.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock('@/lib/api/admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/admin')>()),
  listTenants: (...a: unknown[]) => h.list(...a),
}));

import TenantsPage from './TenantsPage';

const t = (key: string) => i18n.t(key);

function row() {
  return {
    id: 't-1',
    token: 'acme',
    name: 'Acme Corp',
    enabled: true,
    config: null,
    purgeState: null,
    purgeEpoch: null,
    tier: { token: 'gold', color: null },
  };
}

describe('TenantsPage', () => {
  beforeEach(() => {
    h.list.mockReset();
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<TenantsPage />);
    expect(screen.getByRole('status').textContent).toBe(t('tenants:loading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when nothing exists', async () => {
    h.list.mockResolvedValue([]);
    renderListPage(<TenantsPage />);
    expect(await screen.findByText(t('tenants:empty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<TenantsPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('tenants:empty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a row that opens its detail route', async () => {
    h.list.mockResolvedValue([row()]);
    renderListPage(<TenantsPage />);
    const rowEl = await screen.findByRole('button', { name: /acme/ });
    expect(screen.getByRole('table')).toBeTruthy();
    fireEvent.click(rowEl);
    expect(screen.getByTestId('where').textContent).toBe('/admin/tenants/acme');
  });
});
