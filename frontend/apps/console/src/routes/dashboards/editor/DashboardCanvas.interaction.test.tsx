// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The canvas's cell-snapped editing: what a drag, a resize, an offset drag, the placement control
// and the keyboard do to the definition, and what is drawn while a gesture runs. The arithmetic is
// in canvasGeometry and @devicechain/dashboards; these tests pin the wiring between a pointer and
// those transforms.

import '@/i18n/config';
import {
  basePlacement,
  type DashboardDefinition,
  type WidgetDataSource,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { DashboardCanvas } from './DashboardCanvas';

afterEach(cleanup);

// A 1200px canvas of 12 columns with no gutter: a cell is 100px and a stride 100px.
beforeAll(() => {
  (globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, value: 1200 });
});

const quiet: WidgetDataSource = {
  subscribeWidget: () => () => {},
  subscribeAlarms: () => () => {},
  subscribeCommands: () => () => {},
  subscribeLocations: () => () => {},
  isDatasourceAvailable: async () => true,
} satisfies WidgetDataSource;

const card = (id: string, col: number, row: number, cols = 3, rows = 2): WidgetInstance => ({
  id,
  type: 'latest-card',
  layout: { base: { mode: 'grid', col, row, cols, rows } },
  options: { title: id },
});

function board(widgets: WidgetInstance[]): DashboardDefinition {
  return {
    schemaVersion: 2,
    title: 'Canvas',
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 0, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets,
  };
}

function mount(definition: DashboardDefinition, selectedId: string | null = null) {
  const onChange = vi.fn();
  const onSelect = vi.fn();
  const utils = render(
    <DashboardCanvas
      definition={definition}
      onChange={onChange}
      hub={quiet}
      bindings={{}}
      selectedId={selectedId}
      onSelect={onSelect}
    />,
  );
  return { onChange, onSelect, ...utils };
}

const widgetEl = (id: string) => screen.getByTestId(`canvas-widget-${id}`);
const px = (el: HTMLElement, prop: 'left' | 'top' | 'width' | 'height') => parseFloat(el.style[prop]);

// Pointer events dispatched the way a browser delivers them: down on the element, move and up on
// the window.
function press(el: Element, x = 0, y = 0, shiftKey = false) {
  fireEvent.pointerDown(el, { button: 0, clientX: x, clientY: y, shiftKey });
}
function moveTo(x: number, y: number, shiftKey = false) {
  act(() => {
    window.dispatchEvent(new MouseEvent('pointermove', { clientX: x, clientY: y, shiftKey }));
  });
}
function release(x: number, y: number, shiftKey = false) {
  act(() => {
    window.dispatchEvent(new MouseEvent('pointerup', { clientX: x, clientY: y, shiftKey }));
  });
}
const placementOf = (def: DashboardDefinition, id: string) => basePlacement(def.widgets.find((w) => w.id === id)!);

describe('dragging a grid widget', () => {
  const def = () => board([card('a', 0, 0), card('b', 3, 0)]);

  it('shows a ghost snapped to the cells, pushes the neighbour aside live, and commits on release', () => {
    const { onChange } = mount(def());
    press(widgetEl('a'), 10, 10);
    // 3.4 cells right and half a cell down: the widget follows the pointer freely...
    moveTo(10 + 340, 10 + 40);
    expect(px(widgetEl('a'), 'left')).toBe(340);
    // ...the ghost is snapped to the cells it would take (col 3, row 0)...
    const ghost = screen.getByTestId('drag-ghost');
    expect([px(ghost, 'left'), px(ghost, 'top'), px(ghost, 'width'), px(ghost, 'height')]).toEqual([300, 0, 300, 200]);
    // ...and `b`, which that footprint covers, has been pushed below it already.
    expect(px(widgetEl('b'), 'top')).toBe(200);
    expect(onChange).not.toHaveBeenCalled();

    release(10 + 340, 10 + 40);
    expect(onChange).toHaveBeenCalledTimes(1);
    const next = onChange.mock.calls[0][0] as DashboardDefinition;
    expect(placementOf(next, 'a')).toMatchObject({ col: 3, row: 0 });
    expect(placementOf(next, 'b')).toMatchObject({ col: 3, row: 2 });
    expect(screen.queryByTestId('drag-ghost')).toBeNull();
  });

  it('compacts live: a widget below falls into the space the dragged one leaves', () => {
    mount(board([card('a', 0, 0), card('c', 0, 2)]));
    press(widgetEl('a'));
    moveTo(600, 0);
    expect(px(widgetEl('c'), 'top')).toBe(0);
    // Back where it started: the neighbour returns, because the preview is recomputed from the
    // original rather than accumulated.
    moveTo(0, 0);
    expect(px(widgetEl('c'), 'top')).toBe(200);
  });

  it('Escape discards the gesture', () => {
    const { onChange } = mount(def());
    press(widgetEl('a'));
    moveTo(300, 0);
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    release(300, 0);
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.queryByTestId('drag-ghost')).toBeNull();
  });

  it('a click that does not move commits nothing', () => {
    const { onChange, onSelect } = mount(def());
    press(widgetEl('a'));
    release(0, 0);
    expect(onSelect).toHaveBeenCalledWith('a');
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe('resizing', () => {
  it('snaps to cells, reads the size out while it runs, and commits it', () => {
    const { onChange } = mount(board([card('a', 0, 0)]), 'a');
    expect(screen.queryByTestId('size-readout')).toBeNull();
    press(screen.getByTestId('handle-se'), 0, 0);
    moveTo(160, 90); // +1.6 cells, +0.9 cell -> +2, +1
    expect(screen.getByTestId('size-readout').textContent).toBe('5×3');
    expect(px(widgetEl('a'), 'width')).toBe(500);
    release(160, 90);
    expect(placementOf(onChange.mock.calls[0][0], 'a')).toMatchObject({ cols: 5, rows: 3 });
  });

  it('stops at the widget limits, and the readout shows where it stopped', () => {
    // latest-card: 3x2 at least, 6x4 at most.
    mount(board([card('a', 0, 0, 4, 3)]), 'a');
    press(screen.getByTestId('handle-se'));
    moveTo(-900, -900);
    expect(screen.getByTestId('size-readout').textContent).toBe('3×2');
    moveTo(900, 900);
    expect(screen.getByTestId('size-readout').textContent).toBe('6×4');
  });

  it('a west handle keeps the right edge where it was', () => {
    const { onChange } = mount(board([card('a', 4, 0, 3, 2)]), 'a');
    press(screen.getByTestId('handle-w'));
    moveTo(-200, 0);
    release(-200, 0);
    expect(placementOf(onChange.mock.calls[0][0], 'a')).toMatchObject({ col: 2, cols: 5 });
  });

  it('has handles only on the axes the widget allows', () => {
    // entity-selector resizes horizontally only: rows are pinned.
    const picker = { ...card('p', 0, 0, 4, 1), type: 'entity-selector' } as WidgetInstance;
    mount(board([picker]), 'p');
    expect(screen.getByTestId('handle-e')).toBeTruthy();
    expect(screen.getByTestId('handle-w')).toBeTruthy();
    for (const h of ['n', 's', 'ne', 'nw', 'se', 'sw']) expect(screen.queryByTestId(`handle-${h}`)).toBeNull();
  });

  it('shows handles on the selected widget only', () => {
    mount(board([card('a', 0, 0), card('b', 4, 0)]), 'a');
    expect(screen.getAllByTestId('handle-se')).toHaveLength(1);
  });
});

describe('placement mode', () => {
  const two = () => board([card('host', 0, 0, 4, 3), card('badge', 6, 0, 3, 2)]);

  it('Relative asks for a host, then places against the clicked widget', () => {
    const { onChange, onSelect } = mount(two(), 'badge');
    fireEvent.click(screen.getByTestId('placement-relative'));
    expect(screen.getByTestId('pick-host-hint')).toBeTruthy();
    press(widgetEl('host'));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(placementOf(onChange.mock.calls[0][0], 'badge')).toMatchObject({ mode: 'relative', host: 'host' });
    expect(onSelect).toHaveBeenLastCalledWith('badge');
    expect(screen.queryByTestId('pick-host-hint')).toBeNull();
  });

  it('refuses a host that is itself relative, and ends the pick without an edit', () => {
    const d = board([card('host', 0, 0, 4, 3), card('mid', 6, 0, 2, 2), card('leaf', 9, 0, 2, 2)]);
    d.widgets[1] = {
      ...d.widgets[1],
      layout: { base: { mode: 'relative', host: 'host', anchor: 'top-left', size: { cols: 2, rows: 2 } } },
    };
    const { onChange } = mount(d, 'leaf');
    fireEvent.click(screen.getByTestId('placement-relative'));
    press(widgetEl('mid'));
    release(0, 0);
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.queryByTestId('pick-host-hint')).toBeNull();
  });

  it('Escape cancels the host pick without changing anything', () => {
    const { onChange } = mount(two(), 'badge');
    fireEvent.click(screen.getByTestId('placement-relative'));
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(screen.queryByTestId('pick-host-hint')).toBeNull();
    press(widgetEl('host'));
    release(0, 0);
    expect(onChange).not.toHaveBeenCalled();
  });

  const relative = (): DashboardDefinition => {
    const d = two();
    d.widgets[1] = {
      ...d.widgets[1],
      layout: { base: { mode: 'relative', host: 'host', anchor: 'top-right', size: { cols: 2, rows: 2 } } },
    };
    return d;
  };

  it('a relative widget shows the 9-point anchor picker and a dashed guide to its host', () => {
    const { onChange } = mount(relative(), 'badge');
    expect(screen.getAllByTestId(/^anchor-/)).toHaveLength(9);
    expect(screen.getByTestId('anchor-top-right').getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByTestId('guide-badge').querySelector('line')?.getAttribute('stroke-dasharray')).toBeTruthy();
    fireEvent.click(screen.getByTestId('anchor-bottom-left'));
    expect(placementOf(onChange.mock.calls[0][0], 'badge')).toMatchObject({ anchor: 'bottom-left' });
  });

  it('Grid brings a relative widget back onto the grid', () => {
    const { onChange } = mount(relative(), 'badge');
    fireEvent.click(screen.getByTestId('placement-grid'));
    expect(placementOf(onChange.mock.calls[0][0], 'badge').mode).toBe('grid');
  });

  it('dragging a relative widget moves its offset in whole cells, and quarter cells with Shift', () => {
    const { onChange } = mount(relative(), 'badge');
    press(widgetEl('badge'), 0, 0);
    moveTo(140, 0); // 1.4 cells
    release(140, 0);
    expect(placementOf(onChange.mock.calls[0][0], 'badge')).toMatchObject({ offset: { x: 1, y: 0 } });

    press(widgetEl('badge'), 0, 0, true);
    moveTo(140, 0, true);
    release(140, 0, true);
    expect(placementOf(onChange.mock.calls[1][0], 'badge')).toMatchObject({ offset: { x: 1.5, y: 0 } });
  });
});

describe('inline lint findings', () => {
  it('shows a widget that is outside its limits as such, on the widget', () => {
    mount(board([card('big', 0, 0, 12, 8), card('ok', 0, 8)]));
    const badges = screen.getByTestId('widget-lint-big');
    expect(badges.textContent).toContain('Size is outside what this widget supports');
    expect(screen.queryByTestId('widget-lint-ok')).toBeNull();
  });
});

describe('keyboard', () => {
  it('arrow keys move the focused widget one cell, Shift+arrow resizes it', () => {
    const { onChange } = mount(board([card('a', 2, 2)]));
    fireEvent.keyDown(widgetEl('a'), { key: 'ArrowRight' });
    expect(placementOf(onChange.mock.calls[0][0], 'a')).toMatchObject({ col: 3, row: 2, cols: 3 });
    fireEvent.keyDown(widgetEl('a'), { key: 'ArrowDown', shiftKey: true });
    expect(placementOf(onChange.mock.calls[1][0], 'a')).toMatchObject({ col: 2, row: 2, rows: 3 });
  });

  it('is focusable, named with its size and position, and announces the result', () => {
    mount(board([card('a', 2, 2)]));
    const el = widgetEl('a');
    expect(el.getAttribute('tabindex')).toBe('0');
    expect(el.getAttribute('aria-label')).toContain('3×2');
    expect(el.getAttribute('aria-label')).toContain('column 3, row 3');
    fireEvent.keyDown(el, { key: 'ArrowLeft' });
    expect(screen.getByText('a is now 3×2 at column 2, row 3')).toBeTruthy();
  });

  it('does not move past the edge of the canvas', () => {
    const { onChange } = mount(board([card('a', 0, 0)]));
    fireEvent.keyDown(widgetEl('a'), { key: 'ArrowLeft' });
    expect(onChange).not.toHaveBeenCalled();
  });
});
