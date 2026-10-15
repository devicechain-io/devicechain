// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A board stored in the previous grid format is converted on load, approximately, and the
// conversion is never silent: the editor says so until the next save, and that save is what
// writes the current format and retires the notice.

import '@/i18n/config';
import { parseDashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ updateDashboard: vi.fn() }));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: () => null,
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));
vi.mock('./DashboardCanvas', () => ({ DashboardCanvas: () => null }));
vi.mock('./VersionHistorySheet', () => ({ VersionHistorySheet: () => null }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['dashboard:write'] } }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => false) }));
vi.mock('@/lib/api/dashboards', () => ({ updateDashboard: api.updateDashboard, deleteDashboard: vi.fn(), CONFLICT_MARKER: 'CONFLICT' }));

import { DashboardWorkspace } from './DashboardWorkspace';

afterEach(() => {
  cleanup();
  api.updateDashboard.mockReset();
});

// A board as the previous release stored it: 24 fluid columns, 40 px rows, z-ordered boxes.
const stored = {
  schemaVersion: 1,
  title: 'Old board',
  canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [{ id: 'l', type: 'label', layout: { base: { col: 0, colSpan: 8, row: 0, rowSpan: 2, z: 0 } }, options: { text: 'hi' } }],
};

function mount(raw: unknown) {
  render(
    <MemoryRouter>
      <DashboardWorkspace token="dash-1" name="Old board" updatedAt={null} loaded={parseDashboardDefinition(raw)} resolver={{} as DeviceResolver} />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole('button', { name: /edit/i }));
}

const notice = () => screen.queryByText(/Converted from the previous grid/);
const saveButton = () => screen.getByRole('button', { name: /^save$/i });

describe('a board converted from the previous grid', () => {
  it('says so in the editor, and can be saved before anything else is edited', () => {
    mount(stored);
    expect(notice()).not.toBeNull();
    expect((saveButton() as HTMLButtonElement).disabled).toBe(false);
  });

  it('saves the current format, after which the notice is gone', async () => {
    api.updateDashboard.mockResolvedValue({ updatedAt: '2026-10-14T00:00:00Z' });
    mount(stored);
    await act(async () => {
      fireEvent.click(saveButton());
    });
    const sent = JSON.parse(api.updateDashboard.mock.calls[0][1].definition as string);
    expect(sent.schemaVersion).toBe(2);
    expect('convertedFrom' in sent).toBe(false);
    expect(sent.widgets[0].layout.base).toEqual({ mode: 'grid', col: 0, row: 0, cols: 4, rows: 1 });
    expect(notice()).toBeNull();
    expect((saveButton() as HTMLButtonElement).disabled).toBe(true);
  });

  it('shows nothing for a board already in the current format', () => {
    mount({ ...stored, schemaVersion: 2, canvas: { sizing: 'fill' }, widgets: [] });
    expect(notice()).toBeNull();
    expect((saveButton() as HTMLButtonElement).disabled).toBe(true);
  });
});
