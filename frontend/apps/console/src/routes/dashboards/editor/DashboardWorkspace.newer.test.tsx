// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A rollback can land the server's draft on a board saved by a newer release. The editor must
// not carry on over its old copy -- that is a Save button that writes the old board back over
// the newer one -- so it closes on the refusal instead.

import '@/i18n/config';
import { CURRENT_SCHEMA_VERSION, type DashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  rolledBack: undefined as ((r: { definition: string; updatedAt: string | null }) => void) | undefined,
}));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: () => null,
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));
vi.mock('./DashboardCanvas', () => ({ DashboardCanvas: () => null }));
vi.mock('./VersionHistorySheet', () => ({
  VersionHistorySheet: (props: { onRolledBack: typeof h.rolledBack }) => {
    h.rolledBack = props.onRolledBack;
    return null;
  },
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['dashboard:write'] } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => false) }));
vi.mock('@/lib/api/dashboards', () => ({
  updateDashboard: vi.fn(),
  deleteDashboard: vi.fn(),
  CONFLICT_MARKER: 'CONFLICT',
}));

import { DashboardWorkspace } from './DashboardWorkspace';

const loaded: DashboardDefinition = {
  schemaVersion: CURRENT_SCHEMA_VERSION,
  title: 'Workspace',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [],
};

const mount = () =>
  render(
    <MemoryRouter>
      <DashboardWorkspace token="dash-1" name="Workspace" updatedAt={null} loaded={loaded} resolver={{} as DeviceResolver} />
    </MemoryRouter>,
  );

const rollbackTo = (schemaVersion: number) =>
  act(() =>
    h.rolledBack?.({ definition: JSON.stringify({ schemaVersion, title: 'T', widgets: [] }), updatedAt: 'now' }),
  );

afterEach(cleanup);

describe('rolling back to a board from a newer version', () => {
  it('closes the editor on the refusal: no Save is offered over the newer board', () => {
    mount();
    fireEvent.click(screen.getByRole('button', { name: /edit/i }));
    expect(screen.getByRole('button', { name: 'Save' })).toBeTruthy(); // control: the editor offers it

    rollbackTo(CURRENT_SCHEMA_VERSION + 1);

    expect(screen.getByText(/saved by a newer version of DeviceChain\. Upgrade this console/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  });

  it('keeps the editor on a rollback to the current version', () => {
    mount();
    fireEvent.click(screen.getByRole('button', { name: /edit/i }));
    rollbackTo(CURRENT_SCHEMA_VERSION);
    expect(screen.queryByText(/newer version of DeviceChain/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Save' })).toBeTruthy();
  });
});
