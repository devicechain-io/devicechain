// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Fit to limits" in the editor toolbar: offered while the working board has a placement the
// publish would refuse on layout, clamps every widget into its limits and lands as an ordinary
// unsaved edit. The canvas is replaced by a recorder; what is measured is the definition it is
// handed.

import '@/i18n/config';
import { basePlacement, type DashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ definition: undefined as DashboardDefinition | undefined }));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: () => null,
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));
vi.mock('./DashboardCanvas', () => ({
  DashboardCanvas: (props: { definition: DashboardDefinition }) => {
    h.definition = props.definition;
    return null;
  },
}));
vi.mock('./VersionHistorySheet', () => ({ VersionHistorySheet: () => null }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['dashboard:write'] } }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => false) }));
vi.mock('@/lib/api/dashboards', () => ({ updateDashboard: vi.fn(), deleteDashboard: vi.fn(), CONFLICT_MARKER: 'CONFLICT' }));

import { DashboardWorkspace } from './DashboardWorkspace';

afterEach(cleanup);

const board = (cols: number, rows: number): DashboardDefinition => ({
  schemaVersion: 2,
  title: 'Fit',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [
    {
      id: 'g',
      type: 'gauge',
      layout: { base: { mode: 'grid', col: 0, row: 0, cols, rows } },
      options: { title: 'g', min: 0, max: 10 },
    },
  ],
});

function edit(loaded: DashboardDefinition) {
  render(
    <MemoryRouter>
      <DashboardWorkspace token="d" name="Fit" updatedAt={null} loaded={loaded} resolver={{} as DeviceResolver} />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
}

describe('fit to limits in the editor', () => {
  it('is offered for a board outside its limits, clamps it, and then goes away', () => {
    edit(board(12, 12)); // gauge: 3x3 at least, 6x6 at most
    fireEvent.click(screen.getByTestId('fit-to-limits'));
    const p = basePlacement(h.definition!.widgets[0]);
    expect(p).toMatchObject({ mode: 'grid', cols: 6, rows: 6 });
    expect(screen.queryByTestId('fit-to-limits')).toBeNull();
    // It is an edit, not a save: the Save button is armed.
    expect(screen.getByRole('button', { name: /save/i }).hasAttribute('disabled')).toBe(false);
  });

  it('is not offered when everything already fits', () => {
    edit(board(4, 4));
    expect(screen.queryByTestId('fit-to-limits')).toBeNull();
  });
});
