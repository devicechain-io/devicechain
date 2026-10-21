// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A board opened by a deep link in the console: `?bind.<slot>=<device>` binds the device after
// it is checked against the caller's own scope, a pick rewrites the address by REPLACING the
// history entry, and a link that cannot be honoured shows an explicit state instead of the board.

import '@/i18n/config';
import type { DashboardDefinition, DeviceResolver, SelectionTarget, SlotBinding } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  view: [] as Array<Record<string, SlotBinding> | undefined>,
  select: undefined as ((t: SelectionTarget) => void) | undefined,
}));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: (props: { bindings?: Record<string, SlotBinding>; select?: (t: SelectionTarget) => void }) => {
    h.view.push(props.bindings);
    h.select = props.select;
    return <div data-testid="renderer" />;
  },
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));
vi.mock('./DashboardCanvas', () => ({ DashboardCanvas: () => null }));
vi.mock('./VersionHistorySheet', () => ({ VersionHistorySheet: () => null }));
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

const dev = (deviceToken: string): SlotBinding => ({ kind: 'device', deviceToken });
const last = <T,>(xs: T[]): T | undefined => xs[xs.length - 1];

const loaded: DashboardDefinition = {
  schemaVersion: 2,
  title: 'Workspace',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device', defaultBinding: dev('hauler-1') } },
  widgets: [
    {
      id: 'card',
      type: 'latest-card',
      layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } },
      datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] },
    },
  ],
};

let nav: ReturnType<typeof useNavigate> | undefined;
function Probe() {
  const loc = useLocation();
  nav = useNavigate();
  return <div data-testid="search">{loc.search}</div>;
}

function mount(entry: string, deviceExists: (t: string) => Promise<boolean> = async () => true) {
  const resolver = { deviceExists, devicesForAnchor: async () => [] } as unknown as DeviceResolver;
  render(
    <MemoryRouter initialEntries={['/dashboards/dash-1', entry]} initialIndex={1}>
      <Probe />
      <DashboardWorkspace token="dash-1" name="Workspace" updatedAt={null} loaded={loaded} resolver={resolver} />
    </MemoryRouter>,
  );
}

const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });

beforeEach(() => {
  h.view.length = 0;
  h.select = undefined;
});
afterEach(cleanup);

describe('the console workspace opened by a deep link', () => {
  it('binds a valid device before the board first renders', async () => {
    mount('/dashboards/dash-1?bind.machine=hauler-2');
    await flush();
    await flush();
    expect(last(h.view)).toEqual({ machine: dev('hauler-2') });
    // The board never rendered the default device under this link.
    expect(h.view.some((b) => b?.machine && JSON.stringify(b.machine).includes('hauler-1'))).toBe(false);
  });

  it('shows an explicit state for a device the caller cannot see, and no board', async () => {
    mount('/dashboards/dash-1?bind.machine=someone-elses', async () => false);
    await flush();
    await flush();
    expect(screen.getByTestId('bound-devices-notice').textContent).toMatch(/someone-elses/);
    expect(screen.queryByTestId('renderer')).toBeNull();
    expect(h.view).toHaveLength(0);
  });

  it('a pick rewrites the address by replacing the entry, so Back leaves the board', async () => {
    mount('/dashboards/dash-1');
    await flush();
    act(() => h.select?.({ slot: 'machine', binding: dev('hauler-2') }));
    await flush();
    expect(screen.getByTestId('search').textContent).toBe('?bind.machine=hauler-2');

    // Replaced, not pushed: one step back lands on the entry BEFORE the board, not on the
    // board without the pick.
    act(() => nav?.(-1));
    await flush();
    expect(screen.getByTestId('search').textContent).toBe('');
  });

  it('follows an address that changes from outside', async () => {
    mount('/dashboards/dash-1?bind.machine=hauler-2');
    await flush();
    await flush();
    act(() => nav?.('/dashboards/dash-1?bind.machine=hauler-3'));
    await flush();
    await flush();
    expect(last(h.view)).toEqual({ machine: dev('hauler-3') });
  });

  it('dismiss opens the board on its defaults and cleans the address', async () => {
    mount('/dashboards/dash-1?bind.machine=gone', async () => false);
    await flush();
    await flush();
    fireEvent.click(screen.getByTestId('bound-devices-dismiss'));
    await flush();
    expect(last(h.view)).toEqual({ machine: dev('hauler-1') });
    expect(screen.getByTestId('search').textContent).toBe('');
  });
});
