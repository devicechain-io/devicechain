// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Undo, redo, duplicate, copy and paste in the editor, end to end through the real canvas: a
// pointer gesture, the keyboard and the toolbar act on the board, and what is measured is where the
// widgets are drawn, what the Save button says about the working copy, and what a screen reader is
// told. The pure history and clipboard transforms are pinned in @devicechain/dashboards.

import '@/i18n/config';
import type { DashboardDefinition, DeviceResolver, WidgetDataSource, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  toast: (() => undefined) as (...args: unknown[]) => void,
  rolledBack: undefined as ((result: { definition: string; updatedAt: string | null }) => void) | undefined,
  // The tenant the signed-in token names: one tab can change it, and its clipboard outlives that.
  tenant: 'tenant-a',
}));

const quiet: WidgetDataSource = {
  subscribeWidget: () => () => {},
  subscribeAlarms: () => () => {},
  subscribeCommands: () => () => {},
  subscribeLocations: () => () => {},
  isDatasourceAvailable: async () => true,
} satisfies WidgetDataSource;

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: () => null,
  useDashboardHub: () => quiet,
  useSlotCandidates: () => undefined,
}));
vi.mock('./VersionHistorySheet', () => ({
  VersionHistorySheet: (props: { onRolledBack: typeof h.rolledBack }) => {
    h.rolledBack = props.onRolledBack;
    return null;
  },
}));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['dashboard:write'], tenant: h.tenant } }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: (...args: unknown[]) => h.toast(...args) }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => false) }));
vi.mock('@/lib/api/dashboards', () => ({
  updateDashboard: vi.fn(async () => ({ updatedAt: '2026-10-15T00:00:00Z' })),
  deleteDashboard: vi.fn(),
  CONFLICT_MARKER: 'CONFLICT',
}));

import { DashboardWorkspace } from './DashboardWorkspace';
import { clearWidgetClipboard } from './widgetClipboard';

// A 1200px canvas of 12 columns with no gutter: a cell, and a stride, is 100px.
beforeAll(() => {
  (globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, value: 1200 });
});

let toasts: unknown[][] = [];
beforeEach(() => {
  toasts = [];
  h.toast = (...args: unknown[]) => toasts.push(args);
  h.tenant = 'tenant-a';
  clearWidgetClipboard();
});
afterEach(cleanup);

const card = (id: string, col: number, row: number, slot?: string): WidgetInstance => ({
  id,
  type: 'latest-card',
  layout: { base: { mode: 'grid', col, row, cols: 3, rows: 2 } },
  options: { title: id },
  ...(slot ? { datasource: { kind: 'slot' as const, slot, measurements: ['level'] } } : {}),
});

function board(widgets: WidgetInstance[], extra: Partial<DashboardDefinition> = {}): DashboardDefinition {
  return {
    schemaVersion: 3,
    title: 'Canvas',
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 0, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets,
    ...extra,
  };
}

function edit(loaded: DashboardDefinition, token = 'board-1') {
  const utils = render(
    <MemoryRouter>
      <DashboardWorkspace token={token} name="Canvas" updatedAt={null} loaded={loaded} resolver={{} as DeviceResolver} />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
  return utils;
}

const widgetEl = (id: string) => screen.getByTestId(`canvas-widget-${id}`);
const canvasWidgets = () => screen.queryAllByTestId(/^canvas-widget-/);
const left = (el: HTMLElement) => parseFloat(el.style.left);
const undoButton = () => screen.getByTestId('undo');
const redoButton = () => screen.getByTestId('redo');
const saveDisabled = () => screen.getByRole('button', { name: /^save$/i }).hasAttribute('disabled');
const titleInput = () => screen.getByLabelText(/^title$/i) as HTMLInputElement;
const announcer = () => screen.getByTestId('editor-announcer').textContent;

const POINTER = { pointerId: 1, isPrimary: true, pointerType: 'mouse' };
function drag(el: Element, path: [number, number][]) {
  fireEvent.pointerDown(el, { button: 0, buttons: 1, clientX: 0, clientY: 0, ...POINTER });
  for (const [x, y] of path) {
    act(() => {
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: x, clientY: y, buttons: 1, ...POINTER }));
    });
  }
  const [x, y] = path[path.length - 1];
  act(() => {
    window.dispatchEvent(new PointerEvent('pointerup', { clientX: x, clientY: y, ...POINTER }));
  });
}

function key(target: Element, k: string, mods: { ctrlKey?: boolean; shiftKey?: boolean; metaKey?: boolean } = {}) {
  fireEvent.keyDown(target, { key: k, ...mods });
}

// A copy or paste event as the browser dispatches it, with a clipboard behind it.
function clipboardEvent(type: 'copy' | 'paste', target: Element, data: Record<string, string>) {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, 'clipboardData', {
    value: { setData: (t: string, v: string) => (data[t] = v), getData: (t: string) => data[t] ?? '' },
  });
  act(() => {
    target.dispatchEvent(ev);
  });
  return ev;
}

describe('undo and redo', () => {
  it('a drag is ONE step however many pointer moves it took, and undoing it restores the loaded board exactly', () => {
    edit(board([card('a', 0, 0), card('b', 6, 0)]));
    expect(undoButton().hasAttribute('disabled')).toBe(true);
    drag(widgetEl('a'), [
      [100, 0],
      [200, 0],
      [330, 0],
      [420, 20],
    ]);
    expect(left(widgetEl('a'))).toBe(400);
    expect(saveDisabled()).toBe(false);

    fireEvent.click(undoButton());
    expect(left(widgetEl('a'))).toBe(0);
    // One step: there is nothing further back, and the working copy is the loaded one again (Save
    // compares what it would write, so any difference at all would arm it).
    expect(undoButton().hasAttribute('disabled')).toBe(true);
    expect(saveDisabled()).toBe(true);

    fireEvent.click(redoButton());
    expect(left(widgetEl('a'))).toBe(400);
    expect(redoButton().hasAttribute('disabled')).toBe(true);
  });

  it('walks a mixed sequence back and forward from the keyboard', () => {
    edit(board([card('a', 0, 0)]));
    act(() => widgetEl('a').focus());
    key(widgetEl('a'), 'ArrowRight');
    key(widgetEl('a'), 'ArrowRight');
    fireEvent.change(titleInput(), { target: { value: 'P' } });
    fireEvent.change(titleInput(), { target: { value: 'Pl' } });
    fireEvent.change(titleInput(), { target: { value: 'Plant' } });
    expect(left(widgetEl('a'))).toBe(200);

    // Typing the title was one step.
    key(document.body, 'z', { ctrlKey: true });
    expect(titleInput().value).toBe('Canvas');
    expect(left(widgetEl('a'))).toBe(200);
    // Each arrow press was one.
    key(document.body, 'z', { ctrlKey: true });
    expect(left(widgetEl('a'))).toBe(100);
    key(document.body, 'z', { metaKey: true });
    expect(left(widgetEl('a'))).toBe(0);
    expect(saveDisabled()).toBe(true);

    key(document.body, 'z', { ctrlKey: true, shiftKey: true });
    expect(left(widgetEl('a'))).toBe(100);
    key(document.body, 'y', { ctrlKey: true });
    expect(left(widgetEl('a'))).toBe(200);
    key(document.body, 'z', { metaKey: true, shiftKey: true });
    expect(titleInput().value).toBe('Plant');
  });

  it('leaves Ctrl+Z, Ctrl+Y and Ctrl+D to a field the author is typing in', () => {
    edit(board([card('a', 0, 0)]));
    act(() => widgetEl('a').focus()); // a widget is selected, so Ctrl+D would duplicate it
    fireEvent.change(titleInput(), { target: { value: 'Typed' } });
    // Each key is checked on its own: a Ctrl+Z let through and then undone by a Ctrl+Y let through
    // would otherwise look like nothing happened.
    key(titleInput(), 'z', { ctrlKey: true });
    expect(titleInput().value).toBe('Typed');
    expect(redoButton().hasAttribute('disabled')).toBe(true);
    key(titleInput(), 'y', { ctrlKey: true });
    expect(titleInput().value).toBe('Typed');
    key(titleInput(), 'd', { ctrlKey: true });
    expect(canvasWidgets()).toHaveLength(1);
    expect(undoButton().hasAttribute('disabled')).toBe(false);
  });

  it('leaves the keys, and copy and paste, to a dialog, popover or menu open over the canvas', () => {
    edit(board([card('a', 0, 0)]));
    act(() => widgetEl('a').focus());
    drag(widgetEl('a'), [[200, 0]]);
    expect(left(widgetEl('a'))).toBe(200);
    for (const role of ['dialog', 'menu', 'listbox']) {
      const overlay = document.createElement('div');
      overlay.setAttribute('role', role);
      const button = document.createElement('button');
      overlay.appendChild(button);
      document.body.appendChild(overlay);
      key(button, 'z', { ctrlKey: true });
      expect(left(widgetEl('a'))).toBe(200);
      key(button, 'd', { metaKey: true });
      expect(canvasWidgets()).toHaveLength(1);
      const data: Record<string, string> = {};
      expect(clipboardEvent('copy', button, data).defaultPrevented).toBe(false);
      overlay.remove();
    }
    // The counterweight: the same key on the page undoes.
    key(document.body, 'z', { ctrlKey: true });
    expect(left(widgetEl('a'))).toBe(0);
  });

  it('typing into one widget setting is one undo step', () => {
    edit(board([card('a', 0, 0)]));
    act(() => widgetEl('a').focus());
    // The widget's Title setting, found by what it holds.
    let value = 'a';
    for (const v of ['F', 'Fu', 'Fuel']) {
      fireEvent.change(screen.getByDisplayValue(value), { target: { value: v } });
      value = v;
    }
    fireEvent.click(undoButton());
    expect(screen.queryByDisplayValue('a')).not.toBeNull();
    expect(screen.queryByDisplayValue('Fuel')).toBeNull();
    expect(undoButton().hasAttribute('disabled')).toBe(true);
  });

  it('a rollback replaces the working copy and starts the history again', () => {
    edit(board([card('a', 0, 0)]));
    drag(widgetEl('a'), [[300, 0]]);
    expect(undoButton().hasAttribute('disabled')).toBe(false);
    act(() => h.rolledBack?.({ definition: JSON.stringify(board([card('a', 0, 4)])), updatedAt: null }));
    expect(parseFloat(widgetEl('a').style.top)).toBe(400);
    expect(undoButton().hasAttribute('disabled')).toBe(true);
  });

  it('a rollback refused as a NEWER board records no state: the editor closes, with no undo to offer', () => {
    edit(board([card('a', 0, 0)]));
    drag(widgetEl('a'), [[300, 0]]);
    expect(undoButton().hasAttribute('disabled')).toBe(false);
    act(() => h.rolledBack?.({ definition: JSON.stringify({ ...board([card('a', 0, 4)]), schemaVersion: 99 }), updatedAt: null }));
    expect(screen.getByText(/saved by a newer version of DeviceChain/)).toBeTruthy();
    expect(screen.queryByTestId('undo')).toBeNull();
    expect(canvasWidgets()).toHaveLength(0);
  });

  it('tells a screen reader what it undid and redid, by the widget’s name', () => {
    edit(board([{ ...card('a', 0, 0), options: { title: 'Tank level' } } as WidgetInstance]));
    drag(widgetEl('a'), [[300, 0]]);
    fireEvent.click(undoButton());
    expect(announcer()).toBe('Undid move of Tank level');
    fireEvent.click(redoButton());
    expect(announcer()).toBe('Redid move of Tank level');
  });

  it('brings back a board the publish refuses only when that board was really there', () => {
    // A gauge is 3x3 at the least: 12x12 is outside its limits, as an import can leave it.
    const gauge: WidgetInstance = {
      id: 'g',
      type: 'gauge',
      layout: { base: { mode: 'grid', col: 0, row: 0, cols: 12, rows: 12 } },
      options: { title: 'g', min: 0, max: 10 },
    };
    edit(board([gauge]));
    fireEvent.click(screen.getByTestId('fit-to-limits'));
    expect(screen.queryByTestId('fit-to-limits')).toBeNull();
    fireEvent.click(undoButton());
    expect(screen.queryByTestId('fit-to-limits')).not.toBeNull();
    fireEvent.click(redoButton());
    expect(screen.queryByTestId('fit-to-limits')).toBeNull();
  });

  it('a save retires the conversion notice in every state, so undo cannot bring it back', async () => {
    edit(board([card('a', 0, 0)], { convertedFrom: 1 }));
    expect(screen.getByText(/converted from the previous grid/i)).toBeTruthy();
    fireEvent.change(titleInput(), { target: { value: 'Saved' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }));
    });
    fireEvent.click(undoButton());
    expect(titleInput().value).toBe('Canvas');
    expect(screen.queryByText(/converted from the previous grid/i)).toBeNull();
  });
});

describe('duplicate', () => {
  it('copies the selected widget, with a new id, into the first free slot at its span', () => {
    edit(board([card('a', 0, 0), card('b', 3, 0)]));
    act(() => widgetEl('a').focus());
    fireEvent.click(screen.getByTestId('widget-duplicate'));
    const ids = canvasWidgets().map((el) => el.dataset.testid);
    expect(ids).toHaveLength(3);
    const copy = canvasWidgets().find((el) => !['canvas-widget-a', 'canvas-widget-b'].includes(el.dataset.testid!))!;
    expect(left(copy)).toBe(600);
    expect(parseFloat(copy.style.width)).toBe(300);
    expect(announcer()).toBe('Duplicated a');

    // Ctrl+D duplicates the selection again (the copy, now selected), and is one undo step.
    key(document.body, 'd', { ctrlKey: true });
    expect(canvasWidgets()).toHaveLength(4);
    fireEvent.click(undoButton());
    expect(canvasWidgets()).toHaveLength(3);
  });
});

describe('copy and paste', () => {
  const pumpBoard = () =>
    board([card('a', 0, 0, 'slot-1')], {
      slots: { 'slot-1': { type: 'device', label: 'pump-1', defaultBinding: { kind: 'device', deviceToken: 'pump-1' } } },
    });

  function copyFrom(def: DashboardDefinition, token: string, id: string): string {
    const { unmount } = edit(def, token);
    act(() => widgetEl(id).focus());
    const data: Record<string, string> = {};
    const ev = clipboardEvent('copy', widgetEl(id), data);
    expect(ev.defaultPrevented).toBe(true);
    unmount();
    return data['text/plain'];
  }

  it('pastes onto another board UNBOUND where it has no such slot, and says why', () => {
    const text = copyFrom(pumpBoard(), 'board-1', 'a');
    edit(board([card('x', 0, 0)]), 'board-2');
    const ev = clipboardEvent('paste', document.body, { 'text/plain': text });
    expect(ev.defaultPrevented).toBe(true);
    expect(canvasWidgets()).toHaveLength(2);
    expect(screen.queryByTestId('paste-unbound')?.textContent ?? '').toContain('a was pasted without a data source: this dashboard has no slot slot-1');
    expect(announcer()).toBe('Pasted a');
    // Undoing the paste takes the explanation with it.
    fireEvent.click(undoButton());
    expect(canvasWidgets()).toHaveLength(1);
    expect(screen.queryByTestId('paste-unbound')).toBeNull();
  });

  it('pastes on the same board keeping its binding, with nothing to explain', () => {
    const text = copyFrom(pumpBoard(), 'board-1', 'a');
    edit(pumpBoard(), 'board-1');
    clipboardEvent('paste', document.body, { 'text/plain': text });
    expect(canvasWidgets()).toHaveLength(2);
    expect(screen.queryByTestId('paste-unbound')).toBeNull();
  });

  it('the Paste button pastes on the next board what was copied on the last, in this tab', () => {
    const { unmount } = edit(pumpBoard(), 'board-1');
    act(() => widgetEl('a').focus());
    fireEvent.click(screen.getByTestId('widget-copy'));
    unmount();
    edit(board([]), 'board-2');
    fireEvent.click(screen.getByTestId('paste'));
    expect(canvasWidgets()).toHaveLength(1);
    expect(screen.queryByTestId('paste-unbound')).not.toBeNull();
  });

  it('leaves text that is not a widget, and text pasted into a field, to the browser', () => {
    const text = copyFrom(pumpBoard(), 'board-1', 'a');
    edit(board([]), 'board-2');
    expect(clipboardEvent('paste', document.body, { 'text/plain': 'just some text' }).defaultPrevented).toBe(false);
    expect(clipboardEvent('paste', titleInput(), { 'text/plain': text }).defaultPrevented).toBe(false);
    expect(canvasWidgets()).toHaveLength(0);
  });

  it('a copy made in one tenant keeps no binding in another, even on a look-alike board', () => {
    const text = copyFrom(pumpBoard(), 'board-1', 'a');
    expect(JSON.parse(text).tenant).toBe('tenant-a');
    // The tab changes tenant. The other tenant has a board of the same token whose slot-1 is bound
    // to ITS pump-1: same names, a different machine.
    h.tenant = 'tenant-b';
    edit(pumpBoard(), 'board-1');
    clipboardEvent('paste', document.body, { 'text/plain': text });
    expect(canvasWidgets()).toHaveLength(2);
    expect(screen.queryByTestId('paste-unbound')?.textContent ?? '').toContain('copied in another tenant');
  });

  it('the Paste button applies the same rule to the tab’s own clipboard', () => {
    const { unmount } = edit(pumpBoard(), 'board-1');
    act(() => widgetEl('a').focus());
    fireEvent.click(screen.getByTestId('widget-copy'));
    unmount();
    h.tenant = 'tenant-b';
    edit(pumpBoard(), 'board-1');
    fireEvent.click(screen.getByTestId('paste'));
    expect(canvasWidgets()).toHaveLength(2);
    expect(screen.queryByTestId('paste-unbound')?.textContent ?? '').toContain('copied in another tenant');
  });

  // A copy made by a newer release goes through the same schema guard a load does: refused and
  // said so, never read as this format and pasted drawn wrong, and never "nothing to paste".
  it('refuses a copy made by a NEWER release, pastes nothing and records no undo step', () => {
    const text = copyFrom(pumpBoard(), 'board-1', 'a');
    const env = JSON.parse(text) as { definition: Record<string, unknown> };
    const newer = JSON.stringify({ ...env, definition: { ...env.definition, schemaVersion: 99 } });
    edit(board([]), 'board-2');
    const ev = clipboardEvent('paste', document.body, { 'text/plain': newer });
    expect(ev.defaultPrevented).toBe(true);
    expect(canvasWidgets()).toHaveLength(0);
    expect(undoButton().hasAttribute('disabled')).toBe(true);
    expect(toasts).toContainEqual([expect.stringMatching(/copied in a newer version of DeviceChain/), 'error']);
    expect(toasts.some(([msg]) => /Nothing to paste/.test(String(msg)))).toBe(false);
    // The counterweight: the same copy at this build's version pastes.
    clipboardEvent('paste', document.body, { 'text/plain': text });
    expect(canvasWidgets()).toHaveLength(1);
  });

  it('refuses a widget this console does not have, and names it', () => {
    const text = copyFrom(pumpBoard(), 'board-1', 'a');
    const foreign = text.replace('"type":"latest-card"', '"type":"acme/tank"');
    edit(board([]), 'board-2');
    clipboardEvent('paste', document.body, { 'text/plain': foreign });
    expect(canvasWidgets()).toHaveLength(0);
    expect(toasts.some(([msg, kind]) => kind === 'error' && String(msg).includes('acme/tank'))).toBe(true);
  });
});
