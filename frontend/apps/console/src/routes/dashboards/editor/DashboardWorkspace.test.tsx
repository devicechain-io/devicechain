// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which slot manifest each mode hands its renderer, and WHEN.
//
// Edit mode debounces the manifest the canvas resolves through, so typing into the config
// panel re-subscribes the affected widgets once rather than per keystroke. View mode must
// NOT: a selection (a drill, an entity pick) has to move the board in the same render, not
// a quarter of a second later. Both renderers are replaced by recorders here; what is
// measured is the `bindings` prop they are handed, against a fake clock.

import '@/i18n/config';
import type { DashboardDefinition, DeviceResolver, SelectionTarget, SlotBinding } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  view: [] as Array<Record<string, SlotBinding> | undefined>,
  edit: [] as Array<Record<string, SlotBinding>>,
  select: undefined as ((t: SelectionTarget) => void) | undefined,
  change: undefined as ((next: DashboardDefinition) => void) | undefined,
  definition: undefined as DashboardDefinition | undefined,
}));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: (props: { bindings?: Record<string, SlotBinding>; select?: (t: SelectionTarget) => void }) => {
    h.view.push(props.bindings);
    h.select = props.select;
    return null;
  },
  // No live backend: a hub nothing subscribes to, and no candidate lookups.
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));

vi.mock('./DashboardCanvas', () => ({
  DashboardCanvas: (props: {
    bindings: Record<string, SlotBinding>;
    definition: DashboardDefinition;
    onChange: (next: DashboardDefinition) => void;
  }) => {
    h.edit.push(props.bindings);
    h.change = props.onChange;
    h.definition = props.definition;
    return null;
  },
}));
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

const last = <T,>(xs: T[]): T | undefined => xs[xs.length - 1];

const dev = (deviceToken: string): SlotBinding => ({ kind: 'device', deviceToken });

const loaded: DashboardDefinition = {
  schemaVersion: 1,
  title: 'Workspace',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device', defaultBinding: dev('hauler-1') } },
  widgets: [
    {
      id: 'card',
      type: 'latest-card',
      layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 2, z: 0 } },
      datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] },
    },
  ],
};

function mount() {
  render(
    <MemoryRouter>
      <DashboardWorkspace
        token="dash-1"
        name="Workspace"
        updatedAt={null}
        loaded={loaded}
        resolver={{} as DeviceResolver}
      />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  h.view.length = 0;
  h.edit.length = 0;
  h.select = undefined;
  h.change = undefined;
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('the slot manifest each mode renders with', () => {
  it('moves the view at once on a selection, without waiting out the edit debounce', () => {
    mount();
    expect(last(h.view)).toEqual({ machine: dev('hauler-1') });
    expect(h.select).toBeDefined();

    act(() => h.select?.({ slot: 'machine', binding: dev('hauler-2') }));
    // No clock advance: the very next render already carries the selection.
    expect(last(h.view)).toEqual({ machine: dev('hauler-2') });
  });

  it('debounces a binding edit in edit mode', () => {
    mount();
    fireEvent.click(screen.getByRole('button', { name: /edit/i }));
    expect(last(h.edit)).toEqual({ machine: dev('hauler-1') });
    expect(h.change).toBeDefined();

    const base = h.definition as DashboardDefinition;
    act(() =>
      h.change?.({ ...base, slots: { machine: { type: 'device', defaultBinding: dev('hauler-2') } } }),
    );
    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(last(h.edit)).toEqual({ machine: dev('hauler-1') });

    act(() => {
      vi.advanceTimersByTime(100);
    });
    expect(last(h.edit)).toEqual({ machine: dev('hauler-2') });
  });
});
