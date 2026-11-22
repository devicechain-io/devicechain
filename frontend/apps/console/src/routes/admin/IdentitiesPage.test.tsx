// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The identities list page: loading, empty and error states, and a populated row that
// opens the identity (its address is the route, URL-encoded).

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock('@/lib/api/admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/admin')>()),
  listIdentities: (...a: unknown[]) => h.list(...a),
}));

import IdentitiesPage from './IdentitiesPage';

const t = (key: string) => i18n.t(key);

function row() {
  return {
    id: 'id-1',
    email: 'ops+north@example.com',
    firstName: 'Ada',
    lastName: 'Lovelace',
    enabled: true,
    systemRoles: ['superuser'],
    mfaEnabled: false,
    memberships: [],
  };
}

describe('IdentitiesPage', () => {
  beforeEach(() => {
    h.list.mockReset();
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<IdentitiesPage />);
    expect(screen.getByRole('status').textContent).toBe(t('identities:loading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when nothing exists', async () => {
    h.list.mockResolvedValue([]);
    renderListPage(<IdentitiesPage />);
    expect(await screen.findByText(t('identities:empty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<IdentitiesPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('identities:empty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a row that opens its detail route', async () => {
    h.list.mockResolvedValue([row()]);
    renderListPage(<IdentitiesPage />);
    const rowEl = await screen.findByRole('button', { name: /ops\+north@example\.com/ });
    expect(screen.getByRole('table')).toBeTruthy();
    fireEvent.click(rowEl);
    expect(screen.getByTestId('where').textContent).toBe('/admin/identities/ops%2Bnorth%40example.com');
  });
});
