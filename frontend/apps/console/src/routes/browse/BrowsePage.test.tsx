// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Browse page is three independently loaded regions: the facet axes, the selector
// preview and the saved dynamic groups. Each shows loading, empty and error on its own
// (one failing must not read as another being empty), the saved-groups table lists a
// group, and the "save as group" control is gated on device:write.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({
  facets: vi.fn(),
  groups: vi.fn(),
  preview: vi.fn(),
  selector: null as string | null,
  claims: { authorities: [] as string[], scopeAnchors: [] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: h.claims }),
}));
vi.mock('@/lib/api/facet-keys', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/facet-keys')>()),
  listFacetKeys: (...a: unknown[]) => h.facets(...a),
}));
vi.mock('@/lib/api/browse', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/browse')>()),
  listDynamicGroups: (...a: unknown[]) => h.groups(...a),
  previewSelector: (...a: unknown[]) => h.preview(...a),
}));
// The composer is covered by its own tests; here a selector is simply present or not.
vi.mock('@/lib/selector', async (orig) => ({
  ...(await orig<typeof import('@/lib/selector')>()),
  buildSelector: () => ({ selector: h.selector, issues: [] }),
}));

import BrowsePage from './BrowsePage';

const t = (key: string) => i18n.t(key);

const facet = { id: 'f-1', key: 'zone', label: 'Zone', valueType: 'STRING', values: ['north', 'south'] };
const group = { id: 'g-1', token: 'north-devices', name: 'North devices', selector: 'zone == "north"' };

describe('BrowsePage', () => {
  beforeEach(() => {
    h.facets.mockReset();
    h.facets.mockResolvedValue([facet]);
    h.groups.mockReset();
    h.groups.mockResolvedValue([]);
    h.preview.mockReset();
    h.preview.mockResolvedValue({
      valid: true,
      error: null,
      members: { results: [{ id: 'd-1', token: 'pump-1' }], pagination: { totalRecords: 1 } },
    });
    h.selector = null;
    h.claims = { authorities: ['device:read', 'device:write'], scopeAnchors: [] };
  });
  afterEach(cleanup);

  it('shows the facet axes loading while they are read', () => {
    h.facets.mockReturnValue(never());
    renderListPage(<BrowsePage />);
    expect(screen.getAllByRole('status').map((s) => s.textContent)).toContain(t('browse:loadingFacets'));
  });

  it('says a family has no facets rather than showing an empty axes list', async () => {
    h.facets.mockResolvedValue([]);
    renderListPage(<BrowsePage />);
    expect(await screen.findByText(t('browse:noFacetsForFamily'))).toBeTruthy();
  });

  it('shows the facet error, and not the empty message, when facets fail to load', async () => {
    h.facets.mockImplementation(() => Promise.reject(new GraphQLRequestError('facets unavailable', 500)));
    renderListPage(<BrowsePage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('facets unavailable');
    expect(screen.queryByText(t('browse:noFacetsForFamily'))).toBeNull();
  });

  it('shows the saved groups loading while they are read', () => {
    h.groups.mockReturnValue(never());
    renderListPage(<BrowsePage />);
    expect(screen.getAllByRole('status').map((s) => s.textContent)).toContain(t('browse:loadingGroups'));
  });

  it('says there are no saved groups rather than showing an empty table', async () => {
    renderListPage(<BrowsePage />);
    expect(await screen.findByText(t('browse:noGroups'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the groups error, and not the empty message, when they fail to load', async () => {
    h.groups.mockImplementation(() => Promise.reject(new GraphQLRequestError('groups unavailable', 500)));
    renderListPage(<BrowsePage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('groups unavailable');
    expect(screen.queryByText(t('browse:noGroups'))).toBeNull();
  });

  it('lists a saved group with its selector and a members control', async () => {
    h.groups.mockResolvedValue([group]);
    renderListPage(<BrowsePage />);
    const cell = await screen.findByText('north-devices');
    const row = cell.closest('tr') as HTMLElement;
    expect(row.textContent).toContain('North devices');
    expect(row.textContent).toContain('zone == "north"');
    expect(screen.getByRole('button', { name: new RegExp(t('browse:membersButton')) })).toBeTruthy();
  });

  it('offers saving the selector as a group to a writer', async () => {
    h.selector = 'zone == "north"';
    renderListPage(<BrowsePage />);
    expect(await screen.findByRole('button', { name: new RegExp(t('browse:saveAsGroup')) })).toBeTruthy();
  });

  it('shows a read-only member the matches but no save control', async () => {
    h.selector = 'zone == "north"';
    h.claims = { authorities: ['device:read'], scopeAnchors: [] };
    renderListPage(<BrowsePage />);
    expect(await screen.findByText('pump-1')).toBeTruthy();
    expect(screen.queryByRole('button', { name: new RegExp(t('browse:saveAsGroup')) })).toBeNull();
  });
});
