// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connectors list: loading, empty and error states, a populated row that opens the
// connector, and the write gate: a member without connector:write is offered neither the
// create button nor a per-row delete.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, pageOf, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({
  list: vi.fn(),
  claims: { authorities: [] as string[], scopeAnchors: [] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: h.claims }),
}));
vi.mock('@/lib/api/connectors', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/connectors')>()),
  listConnectors: (...a: unknown[]) => h.list(...a),
}));

import ConnectorsPage from './ConnectorsPage';

const t = (key: string) => i18n.t(key);

function row() {
  return { token: 'ops-hook', name: 'Ops hook', type: 'httpCall', description: 'pages the on-call' };
}

describe('ConnectorsPage', () => {
  beforeEach(() => {
    h.list.mockReset();
    h.claims = { authorities: ['connector:read', 'connector:write'], scopeAnchors: [] };
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<ConnectorsPage />);
    expect(screen.getByRole('status').textContent).toBe(t('connectors:loading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when there are none', async () => {
    h.list.mockResolvedValue(pageOf([]));
    renderListPage(<ConnectorsPage />);
    expect(await screen.findByText(t('connectors:empty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<ConnectorsPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('connectors:empty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a row that opens its detail route', async () => {
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<ConnectorsPage />);
    const rowEl = await screen.findByRole('button', { name: /Ops hook/ });
    fireEvent.click(rowEl);
    expect(screen.getByTestId('where').textContent).toBe('/connectors/ops-hook');
  });

  it('offers create and delete to a writer', async () => {
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<ConnectorsPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('button', { name: new RegExp(t('connectors:newConnector')) })).toBeTruthy();
    expect(screen.getByRole('button', { name: t('connectors:delete') })).toBeTruthy();
  });

  it('offers neither create nor delete to a read-only member, who still sees the row', async () => {
    h.claims = { authorities: ['connector:read'], scopeAnchors: [] };
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<ConnectorsPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('button', { name: /Ops hook/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: new RegExp(t('connectors:newConnector')) })).toBeNull();
    expect(screen.queryByRole('button', { name: t('connectors:delete') })).toBeNull();
  });
});
