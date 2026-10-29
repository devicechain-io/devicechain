// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboard page header offers what the caller may do. Edit, Delete and Clone need
// dashboard:write and no scope (the server refuses a scoped caller every dashboard write), History
// needs dashboard:read and no scope; Export and Preview act on the definition already served and
// stay for everyone.

import '@/i18n/config';
import type { DashboardDefinition, DeviceResolver } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ authorities: [] as string[], scope: [] as string[] }));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: () => null,
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));
vi.mock('./DashboardCanvas', () => ({ DashboardCanvas: () => null }));
vi.mock('./VersionHistorySheet', () => ({ VersionHistorySheet: () => null }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities, scopeAnchors: h.scope } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(() => false) }));
vi.mock('@/lib/api/dashboards', () => ({ updateDashboard: vi.fn(), deleteDashboard: vi.fn(), CONFLICT_MARKER: 'CONFLICT' }));

import { DashboardWorkspace } from './DashboardWorkspace';

const loaded: DashboardDefinition = {
  schemaVersion: 3,
  title: 'Board',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 0, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [],
};

function header(authorities: string[], scope: string[] = []) {
  h.authorities = authorities;
  h.scope = scope;
  render(
    <MemoryRouter>
      <DashboardWorkspace token="b" name="Board" updatedAt={null} loaded={loaded} resolver={{} as DeviceResolver} />
    </MemoryRouter>,
  );
}
const has = (name: RegExp) => screen.queryByRole('button', { name }) !== null;

const openExport = () =>
  fireEvent.pointerDown(screen.getByRole('button', { name: /export/i }), { button: 0, ctrlKey: false, pointerType: 'mouse' });

afterEach(cleanup);

describe('dashboard page header', () => {
  it('offers a customer user only the read affordances', () => {
    header(['dashboard:read'], ['customer-1']);
    expect(has(/^edit$/i)).toBe(false);
    expect(has(/delete/i)).toBe(false);
    expect(has(/history/i)).toBe(false);
    expect(has(/preview/i)).toBe(true);
    openExport();
    expect(screen.queryByRole('menuitem', { name: /clone/i })).toBeNull();
    expect(screen.getByRole('menuitem', { name: /download/i })).toBeTruthy();
  });

  it('keeps writes from a scoped member whose role names dashboard:write', () => {
    header(['dashboard:read', 'dashboard:write'], ['customer-1']);
    expect(has(/^edit$/i)).toBe(false);
    expect(has(/delete/i)).toBe(false);
    expect(has(/history/i)).toBe(false);
  });

  it('offers an unscoped read-only member history but no write', () => {
    header(['dashboard:read']);
    expect(has(/^edit$/i)).toBe(false);
    expect(has(/delete/i)).toBe(false);
    expect(has(/history/i)).toBe(true);
    expect(has(/preview/i)).toBe(true);
  });

  it('offers a writer all of it', () => {
    header(['dashboard:read', 'dashboard:write']);
    expect(has(/^edit$/i)).toBe(true);
    expect(has(/delete/i)).toBe(true);
    expect(has(/history/i)).toBe(true);
    openExport();
    expect(screen.getByRole('menuitem', { name: /clone/i })).toBeTruthy();
  });
});
