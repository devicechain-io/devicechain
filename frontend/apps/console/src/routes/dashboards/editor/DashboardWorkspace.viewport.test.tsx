// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import i18n from '@/i18n/config';
import { serializeDefinition, type DashboardDefinition, type DeviceResolver, type SelectionTarget, type SlotBinding } from '@devicechain/dashboards';
import { createWidgetRegistry, defineWidget } from '@devicechain/widget-sdk';
import type { DashboardRendererProps } from '@devicechain/widgets';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TANK_ID, tankLevelWidget } from './testExtension';
import { layoutFindingsOf } from './layoutFindings';

const h = vi.hoisted(() => ({
  renderer: undefined as Record<string, unknown> | undefined,
  canvas: undefined as DashboardDefinition | undefined,
  hub: {},
  runtime: false,
}));
vi.mock('@devicechain/widgets', async (original) => {
  const widgets = await original<typeof import('@devicechain/widgets')>();
  return {
    ...widgets,
    DashboardRenderer: (props: DashboardRendererProps) => {
      h.renderer = { ...props };
      return h.runtime ? <widgets.DashboardRenderer {...props} /> : <div data-testid="runtime-preview" />;
    },
    useDashboardHub: () => h.hub,
    useSlotCandidates: () => undefined,
  };
});
vi.mock('./DashboardCanvas', () => ({
  DashboardCanvas: ({ definition }: { definition: DashboardDefinition }) => {
    h.canvas = definition;
    return <div data-testid="authoring-canvas" />;
  },
}));
vi.mock('./VersionHistorySheet', () => ({ VersionHistorySheet: () => null }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['dashboard:write'] } }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => false) }));
vi.mock('@/lib/api/dashboards', () => ({ updateDashboard: vi.fn(), deleteDashboard: vi.fn(), CONFLICT_MARKER: 'CONFLICT' }));

import { updateDashboard } from '@/lib/api/dashboards';
import { DashboardWorkspace } from './DashboardWorkspace';

const registry = createWidgetRegistry([defineWidget({
  ...tankLevelWidget,
  descriptor: {
    ...tankLevelWidget.descriptor,
    layout: { defaultSpan: { cols: 8, rows: 2 }, minSpan: { cols: 8, rows: 2 }, resize: 'both' },
  },
})]);
const board: DashboardDefinition = {
  schemaVersion: 2,
  title: 'Viewport fixture',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'machine-1' } } },
  widgets: [{ id: 'tank', type: TANK_ID,
    widget: {
      version: tankLevelWidget.descriptor.version,
      optionsVersion: tankLevelWidget.descriptor.optionsVersion,
      sdkApiVersion: tankLevelWidget.descriptor.sdkApiVersion,
    },
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 8, rows: 2 } },
    datasource: { kind: 'slot', slot: 'machine', measurements: ['level'] } }],
};

function mount() {
  render(<MemoryRouter><DashboardWorkspace token="d" name="Viewport fixture" updatedAt={null}
    loaded={board} resolver={{} as DeviceResolver} registry={registry} /></MemoryRouter>);
}

function pick(name: string) {
  fireEvent.keyDown(screen.getByRole('combobox', { name: 'Preview width' }), { key: 'ArrowDown' });
  fireEvent.click(screen.getByRole('option', { name }));
}

beforeEach(async () => {
  await i18n.changeLanguage('en');
  vi.clearAllMocks();
  h.runtime = false;
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('viewport preview', () => {
  it('lints the runtime footprint: valid wide, below minimum after narrow reflow', () => {
    const original = JSON.stringify(board);
    expect(layoutFindingsOf(board, registry, 1366)).toEqual([]);
    expect(layoutFindingsOf(board, registry, 390)).toEqual([
      expect.objectContaining({ widgetId: 'tank', code: 'span-out-of-limits' }),
    ]);
    expect(JSON.stringify(board)).toBe(original);
    expect(layoutFindingsOf(board, registry)).toEqual([]);
  });

  it('uses the active breakpoint, density, padding and fixed-width cap for findings', () => {
    const responsive: DashboardDefinition = {
      ...board,
      canvas: { ...board.canvas, breakpoints: { base: 0, wide: 1200 } },
      widgets: board.widgets.map((widget) => ({ ...widget, layout: {
        base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 2 },
        wide: { mode: 'grid', col: 0, row: 0, cols: 8, rows: 2 },
      } })),
    };
    expect(layoutFindingsOf(responsive, registry, 1366)).toEqual([]);
    expect(layoutFindingsOf(responsive, registry, 960)).toEqual([
      expect.objectContaining({ code: 'span-out-of-limits' }),
    ]);
    const fixed = { ...responsive, canvas: { ...responsive.canvas, sizing: { width: 390 } } };
    expect(layoutFindingsOf(fixed, registry, 1920)).toEqual([
      expect.objectContaining({ code: 'span-out-of-limits' }),
    ]);
    const padded = { ...board, canvas: { ...board.canvas, grid: { ...board.canvas.grid, padding: 200 } } };
    expect(layoutFindingsOf(padded, registry, 768)).toEqual([
      expect.objectContaining({ code: 'span-out-of-limits' }),
    ]);
    const wall: DashboardDefinition = { ...board, canvas: { ...board.canvas, density: 'wall' } };
    expect(layoutFindingsOf(wall, registry, 600)).toEqual([
      expect.objectContaining({ code: 'span-out-of-limits' }),
    ]);
  });

  it('applies every named width, updates findings, and resets without editing or saving', () => {
    mount();
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
    for (const width of [390, 768, 960, 1366, 1920, 3840]) {
      pick(`${width} px`);
      expect(screen.getByTestId('viewport-surface').style.width).toBe(`${width}px`);
      expect(screen.getByTestId('runtime-preview')).toBeTruthy();
      expect(h.renderer?.definition).toBe(board);
      expect(h.renderer?.hub).toBe(h.hub);
      expect(h.renderer?.select).toBeUndefined();
      expect(h.renderer?.candidates).toBeUndefined();
      expect(h.renderer?.actions).toBeUndefined();
      expect(screen.getByRole('button', { name: /^save$/i }).hasAttribute('disabled')).toBe(true);
      expect(screen.getByTestId('undo').hasAttribute('disabled')).toBe(true);
      if (width === 390) expect(screen.getByText(/Size is outside what this widget supports/)).toBeTruthy();
      else expect(screen.getByText('No layout findings at this width')).toBeTruthy();
    }
    pick('Container');
    expect(screen.queryByTestId('viewport-surface')).toBeNull();
    expect(screen.getByTestId('authoring-canvas')).toBeTruthy();
    expect(screen.queryByText(/Layout preview at/)).toBeNull();
    expect(h.canvas).toBe(board);
    expect(updateDashboard).not.toHaveBeenCalled();
  });

  it('keeps synthetic data preview independent from the selected width', () => {
    mount();
    fireEvent.click(screen.getByRole('button', { name: /^preview$/i }));
    const synthetic = h.renderer?.hub;
    expect(synthetic).not.toBe(h.hub);
    pick('390 px');
    expect(h.renderer?.hub).toBe(synthetic);
    pick('Container');
    expect(h.renderer?.hub).toBe(synthetic);
    fireEvent.click(screen.getByRole('button', { name: /^preview$/i }));
    expect(h.renderer?.hub).toBe(h.hub);
    expect(updateDashboard).not.toHaveBeenCalled();
  });

  it('renders the shared runtime at the selected width and reflows its actual grid', () => {
    const observers = new Set<() => void>();
    vi.stubGlobal('ResizeObserver', class {
      constructor(private notify: () => void) {}
      observe() { observers.add(this.notify); }
      disconnect() { observers.delete(this.notify); }
    });
    // jsdom has no layout engine. Supply the content width that the browser measures,
    // then explicitly deliver its ResizeObserver notification when the host width changes.
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(function (this: HTMLElement) {
      return Number.parseFloat((this.closest('[data-testid="viewport-surface"]') as HTMLElement | null)?.style.width ?? '1366');
    });
    mount();
    fireEvent.click(screen.getByRole('button', { name: /^preview$/i }));
    h.runtime = true;
    pick('390 px');
    expect(screen.getByTestId('viewport-surface').querySelector('[data-columns]')?.getAttribute('data-columns')).toBe('6');
    pick('1366 px');
    act(() => { for (const notify of observers) notify(); });
    expect(screen.getByTestId('viewport-surface').querySelector('[data-columns]')?.getAttribute('data-columns')).toBe('12');
    expect(screen.getByText('No layout findings at this width')).toBeTruthy();
  });

  it('saves authored changes without persisting preview settings', async () => {
    vi.mocked(updateDashboard).mockResolvedValue({ token: 'd', updatedAt: '2026-11-24T00:00:00Z' });
    mount();
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Renamed' } });
    pick('390 px');
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /^save$/i })); });
    expect(updateDashboard).toHaveBeenCalledExactlyOnceWith('d', {
      name: 'Renamed', definition: serializeDefinition({ ...board, title: 'Renamed' }), expectedUpdatedAt: null,
    });
    expect(screen.getByRole('button', { name: /^save$/i }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByTestId('undo').hasAttribute('disabled')).toBe(false);
  });

  it('supports keyboard selection and preserves the live selection across preview and reset', () => {
    mount();
    const select = h.renderer?.select as (target: SelectionTarget) => void;
    act(() => select({ slot: 'machine', binding: { kind: 'device', deviceToken: 'machine-2' } }));
    const trigger = screen.getByRole('combobox', { name: 'Preview width' });
    trigger.focus();
    fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    const option = screen.getByRole('option', { name: '390 px' });
    option.focus();
    fireEvent.keyDown(option, { key: 'Enter' });
    expect(screen.getByTestId('viewport-surface').style.width).toBe('390px');
    expect(h.renderer?.select).toBeUndefined();
    pick('Container');
    expect((h.renderer?.bindings as Record<string, SlotBinding>).machine).toEqual({ kind: 'device', deviceToken: 'machine-2' });
    expect(h.renderer?.select).toBeDefined();
    expect(updateDashboard).not.toHaveBeenCalled();
  });
});
