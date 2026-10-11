// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which copy of a dashboard the detail page asks the server for.
//
// The draft is author-only, so the page must not fetch it for a member who cannot write:
// that call is refused, and the member would see an error where they are entitled to the
// published board. The assertions are about WHICH operation is called, because that is
// the whole of the decision; the workspace is replaced by a recorder.

import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  authorities: [] as string[],
  workspace: [] as Array<{ updatedAt: string | null; name: string | null }>,
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
});
