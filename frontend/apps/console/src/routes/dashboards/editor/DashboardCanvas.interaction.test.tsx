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
import { createHostWidgetRegistry } from '@devicechain/host-widgets';
import { defineWidget } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { createPortal } from 'react-dom';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { DashboardCanvas } from './DashboardCanvas';

// A widget that renders a popover through a portal, as a select or a menu does.
const PORTAL_ID = 'test/portal';
const portalWidget = defineWidget({
  descriptor: {
    id: PORTAL_ID,
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Portal', category: 'Test' },
    channel: 'none',
    datasource: { mode: 'none' },
    binding: {},
    interactions: {},
    options: {},
    defaults: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: () => createPortal(<div data-testid="portal-pop">pop</div>, document.body),
  },
});

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
// the window, all from one primary mouse pointer with its button held.
const POINTER = { pointerId: 1, isPrimary: true, pointerType: 'mouse' };
function press(el: Element, x = 0, y = 0, shiftKey = false) {
  fireEvent.pointerDown(el, { button: 0, buttons: 1, clientX: x, clientY: y, shiftKey, ...POINTER });
}
function moveTo(x: number, y: number, shiftKey = false) {
  act(() => {
    window.dispatchEvent(new PointerEvent('pointermove', { clientX: x, clientY: y, shiftKey, buttons: 1, ...POINTER }));
  });
}
function release(x: number, y: number, shiftKey = false) {
  act(() => {
    window.dispatchEvent(new PointerEvent('pointerup', { clientX: x, clientY: y, shiftKey, ...POINTER }));
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

  it('swaps live: a neighbour the drop covers takes the room the dragged widget left, and returns', () => {
    mount(board([card('a', 0, 0), card('c', 0, 2)]));
    press(widgetEl('a'));
    moveTo(0, 200); // a onto c's rows
    expect(px(widgetEl('c'), 'top')).toBe(0);
    // Back where it started: the neighbour returns, because the preview is recomputed from the
    // original rather than accumulated.
    moveTo(0, 0);
    expect(px(widgetEl('c'), 'top')).toBe(200);
  });

  it('keeps the gaps the author left: a widget the drop does not cover never moves', () => {
    // c sits two rows below a on purpose; a is dragged off to the right, over nothing.
    const { onChange } = mount(board([card('a', 0, 0), card('c', 0, 4)]));
    press(widgetEl('a'));
    moveTo(600, 0);
    expect(px(widgetEl('c'), 'top')).toBe(400);
    release(600, 0);
    expect(placementOf(onChange.mock.calls[0][0], 'c')).toMatchObject({ col: 0, row: 4 });
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

  it('pointercancel, a lost capture and a window blur each discard the preview', () => {
    const { onChange } = mount(def());
    for (const end of ['pointercancel', 'lostpointercapture', 'blur']) {
      press(widgetEl('a'));
      moveTo(300, 0);
      expect(screen.getByTestId('drag-ghost')).toBeTruthy();
      act(() => {
        window.dispatchEvent(end === 'blur' ? new Event(end) : new PointerEvent(end, POINTER));
      });
      expect(screen.queryByTestId('drag-ghost'), end).toBeNull();
      expect(px(widgetEl('b'), 'top'), end).toBe(0);
      release(300, 0);
    }
    expect(onChange).not.toHaveBeenCalled();
  });

  it('a mouse released where no pointerup reached the page commits nothing', () => {
    const { onChange } = mount(def());
    press(widgetEl('a'));
    moveTo(300, 0);
    act(() => {
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 300, clientY: 0, buttons: 0, ...POINTER }));
    });
    expect(screen.queryByTestId('drag-ghost')).toBeNull();
    release(300, 0);
    expect(onChange).not.toHaveBeenCalled();
  });

  it('a second pointer neither starts a gesture nor steers the running one', () => {
    const { onChange } = mount(def());
    const withId = (type: string, x: number, pointerId: number) => {
      const e = new MouseEvent(type, { clientX: x, clientY: 0 });
      Object.defineProperty(e, 'pointerId', { value: pointerId });
      return e;
    };
    const down = new MouseEvent('pointerdown', { bubbles: true, button: 0, clientX: 0, clientY: 0 });
    Object.defineProperty(down, 'pointerId', { value: 1 });
    fireEvent(widgetEl('a'), down);
    // Another finger on b: not primary, and a gesture is already running.
    fireEvent.pointerDown(widgetEl('b'), { button: 0, isPrimary: false, pointerId: 2 });
    act(() => {
      window.dispatchEvent(withId('pointermove', 600, 2));
    });
    expect(px(widgetEl('a'), 'left')).toBe(0);
    act(() => {
      window.dispatchEvent(withId('pointerup', 600, 2));
    });
    expect(onChange).not.toHaveBeenCalled();
    act(() => {
      window.dispatchEvent(withId('pointerup', 300, 1));
    });
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(placementOf(onChange.mock.calls[0][0], 'a')).toMatchObject({ col: 3 });
  });

  it('a pointer down from a popover the widget portals out of its body starts no drag', () => {
    // React bubbles an event out of a portal to the component that rendered it: the widget's own
    // handler sees a pointer down whose target is not inside the widget at all.
    const onChange = vi.fn();
    const onSelect = vi.fn();
    render(
      <DashboardCanvas
        definition={board([{ id: 'p', type: PORTAL_ID, widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 2 } } }])}
        onChange={onChange}
        hub={quiet}
        bindings={{}}
        selectedId={null}
        onSelect={onSelect}
        registry={createHostWidgetRegistry([portalWidget])}
      />,
    );
    press(screen.getByTestId('portal-pop'));
    moveTo(300, 0);
    expect(screen.queryByTestId('drag-ghost')).toBeNull();
    release(300, 0);
    expect(onChange).not.toHaveBeenCalled();
    expect(onSelect).not.toHaveBeenCalled();
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

  it('a host can be picked from the keyboard, and the placed widget keeps the focus', () => {
    const { onChange, onSelect } = mount(two(), 'badge');
    fireEvent.click(screen.getByTestId('placement-relative'));
    widgetEl('host').focus();
    fireEvent.keyDown(widgetEl('host'), { key: 'Enter' });
    expect(placementOf(onChange.mock.calls[0][0], 'badge')).toMatchObject({ mode: 'relative', host: 'host' });
    expect(onSelect).toHaveBeenLastCalledWith('badge');
    expect(document.activeElement).toBe(widgetEl('badge'));
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

  it('Tab leaves the anchor picker: it holds no keyboard trap', () => {
    mount(relative(), 'badge');
    const cells = screen.getAllByTestId(/^anchor-/);
    // Every point is an ordinary tab stop (no roving tabindex, no key handler that eats Tab).
    for (const c of cells) expect(c.getAttribute('tabindex')).not.toBe('-1');
    const last = cells[cells.length - 1];
    last.focus();
    const tab = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    last.dispatchEvent(tab);
    expect(tab.defaultPrevented).toBe(false);
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

  it('keeps the focus on the widget it moved, and announces a relative widget by its offset', () => {
    const d = board([card('host', 0, 0, 4, 3), card('badge', 6, 0, 3, 2), card('a', 2, 4)]);
    d.widgets[1] = {
      ...d.widgets[1],
      layout: { base: { mode: 'relative', host: 'host', anchor: 'top-right', size: { cols: 2, rows: 2 } } },
    };
    const { onChange, rerender } = mount(d);
    widgetEl('a').focus();
    fireEvent.keyDown(widgetEl('a'), { key: 'ArrowDown' });
    rerender(
      <DashboardCanvas definition={onChange.mock.calls[0][0]} onChange={onChange} hub={quiet} bindings={{}} selectedId="a" onSelect={() => {}} />,
    );
    expect(document.activeElement).toBe(widgetEl('a'));
    fireEvent.keyDown(widgetEl('badge'), { key: 'ArrowLeft' });
    expect(screen.getByText('badge is now offset -1, 0 cells from its anchor')).toBeTruthy();
  });

  it('does not move past the edge of the canvas', () => {
    const { onChange } = mount(board([card('a', 0, 0)]));
    fireEvent.keyDown(widgetEl('a'), { key: 'ArrowLeft' });
    expect(onChange).not.toHaveBeenCalled();
  });
});
