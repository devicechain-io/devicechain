// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which copy of a dashboard the detail page asks the server for.
//
// The draft is author-only, so the page must not fetch it for a member who cannot write:
// that call is refused, and the member would see an error where they are entitled to the
// published board. The assertions are about WHICH operation is called, because that is
// the whole of the decision; the workspace is replaced by a recorder.

import i18n from '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  authorities: [] as string[],
  workspace: [] as Array<{ updatedAt: string | null; name: string | null }>,
  preload: vi.fn(),
}));

vi.mock('@devicechain/widgets', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  preloadWidgets: (...a: unknown[]) => h.preload(...a),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities } }),
}));
vi.mock('./editor/DashboardWorkspace', () => ({
  DashboardWorkspace: (props: { updatedAt: string | null; name: string | null }) => {
    h.workspace.push({ updatedAt: props.updatedAt, name: props.name });
    return <div data-testid="workspace" />;
  },
}));
vi.mock('@/lib/api/dashboards', () => ({
  getDashboard: vi.fn(),
  getPublishedDashboard: vi.fn(),
  isNotPublishedError: (e: unknown) => (e as { notPublished?: boolean })?.notPublished === true,
}));

import { getDashboard, getPublishedDashboard } from '@/lib/api/dashboards';
import DashboardDetailPage from './DashboardDetailPage';

const definition = JSON.stringify({ schemaVersion: 1, title: 'T', widgets: [] });

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/dashboards/ops']}>
      <Routes>
        <Route path="/dashboards/:token" element={<DashboardDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  h.workspace.length = 0;
  h.preload.mockReset();
  h.preload.mockResolvedValue(undefined);
  vi.mocked(getDashboard).mockReset();
  vi.mocked(getPublishedDashboard).mockReset();
});
afterEach(cleanup);

describe('DashboardDetailPage', () => {
  it('an author loads the draft and never the published door', async () => {
    h.authorities = ['dashboard:read', 'dashboard:write'];
    vi.mocked(getDashboard).mockResolvedValue({
      token: 'ops',
      name: 'Ops',
      description: null,
      definition,
      updatedAt: '2026-10-10T00:00:00Z',
      publishedVersion: 1,
    });
    renderPage();
    await screen.findByTestId('workspace');
    expect(getDashboard).toHaveBeenCalledWith('ops');
    expect(getPublishedDashboard).not.toHaveBeenCalled();
    expect(h.workspace[h.workspace.length - 1]?.updatedAt).toBe('2026-10-10T00:00:00Z');
  });

  it('a read-only member loads the published snapshot and never the draft', async () => {
    h.authorities = ['dashboard:read'];
    vi.mocked(getPublishedDashboard).mockResolvedValue({
      token: 'ops',
      name: 'Ops',
      description: null,
      version: 2,
      publishedAt: '2026-10-10T00:00:00Z',
      definition,
    });
    renderPage();
    await screen.findByTestId('workspace');
    expect(getPublishedDashboard).toHaveBeenCalledWith('ops');
    expect(getDashboard).not.toHaveBeenCalled();
    // A viewer never saves, so there is no draft baseline to carry.
    expect(h.workspace[h.workspace.length - 1]?.updatedAt).toBeNull();
  });

  it('tells a viewer when the board has not been published yet', async () => {
    h.authorities = ['dashboard:read'];
    vi.mocked(getPublishedDashboard).mockRejectedValue({ notPublished: true });
    renderPage();
    await waitFor(() => expect(screen.getByText(/has not been published yet/)).toBeTruthy());
    expect(screen.queryByTestId('workspace')).toBeNull();
  });

  // The board's lazily loaded widget code arrives before the workspace mounts, so its first frame
  // is the whole board rather than empty boxes filling in. Both doors: the draft and the snapshot.
  for (const door of ['draft', 'published'] as const) {
    it(`holds the ${door} board on its loading state until the widget code has arrived`, async () => {
      h.authorities = door === 'draft' ? ['dashboard:read', 'dashboard:write'] : ['dashboard:read'];
      const withWidgets = JSON.stringify({
        schemaVersion: 1,
        title: 'T',
        widgets: [{ type: 'alarm-table' }, { type: 'battery' }, { nope: 1 }],
      });
      const row = { token: 'ops', name: 'Ops', description: null, definition: withWidgets };
      vi.mocked(getDashboard).mockResolvedValue({ ...row, updatedAt: null, publishedVersion: 1 });
      vi.mocked(getPublishedDashboard).mockResolvedValue({ ...row, version: 2, publishedAt: '2026-10-10T00:00:00Z' });
      let release!: () => void;
      h.preload.mockImplementation(() => new Promise<void>((r) => (release = r)));
      renderPage();
      await waitFor(() => expect(h.preload).toHaveBeenCalledTimes(1));
      expect(h.preload.mock.calls[0][1]).toEqual(['alarm-table', 'battery']);
      // Still loading: the definition below is only fit for reading types, so what replaces the
      // loading state afterwards (a parse error) is beside the point -- that it waits is the point.
      expect(screen.getByText(i18n.t('dashboards:detailLoading'))).toBeTruthy();
      release();
      await waitFor(() => expect(screen.queryByText(i18n.t('dashboards:detailLoading'))).toBeNull());
    });
  }
});
