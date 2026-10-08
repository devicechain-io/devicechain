// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The workspace's handling of a widget whose type this console does not know (a newer
// release added it). The pure parse/serialize contract is pinned in the dashboards
// package; this pins the two places the workspace itself must route a placeholder
// differently from a known widget: selecting it opens the explanatory panel rather than
// the config panel, and copy-as-JSON writes the stored widget, not the placeholder.
//
// Faked here: the auth claims, the dashboard API and the toast — the seam around the
// workspace. The parse, the canvas and the panels run for real.

import '@/i18n/config';
import { parseDashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { toastMock } = vi.hoisted(() => ({ toastMock: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['dashboard:read', 'dashboard:write'] } }),
}));

vi.mock('@/lib/api/dashboards', () => ({
  updateDashboard: vi.fn(),
  deleteDashboard: vi.fn(),
  CONFLICT_MARKER: 'conflict',
}));

vi.mock('@/components/ui/toast', () => ({
  useToast: () => ({ toast: toastMock }),
}));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { DashboardWorkspace } from './DashboardWorkspace';

const box = { col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0 };

// A stored widget of a type this console does not know, with fields the parser does not
// read. Its stored JSON is what an export must contain.
const newer = {
  type: 'card-deck',
  id: 'deck-1',
  layout: { base: box },
  options: { title: 'Fleet', order: 'type' },
  deck: { cards: [{ id: 'hauler' }] },
};

const resolver: DeviceResolver = {
  devicesForAnchor: async () => [],
  deviceExists: async () => true,
};

let clipboardWrites: string[];

// jsdom has no ResizeObserver; the edit canvas measures its width with one.
class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', NoopResizeObserver);
  clipboardWrites = [];
  toastMock.mockReset();
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (text: string) => void clipboardWrites.push(text) },
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderWorkspace() {
  const loaded = parseDashboardDefinition({ title: 'Board', widgets: [newer] });
  return render(
    <MemoryRouter>
      <ConfirmProvider>
        <DashboardWorkspace token="dash-1" name="Board" updatedAt={null} loaded={loaded} resolver={resolver} />
      </ConfirmProvider>
    </MemoryRouter>,
  );
}

// Radix menus open on pointerdown, not click.
function openMenu(trigger: HTMLElement) {
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: 'mouse' });
}

describe('DashboardWorkspace with a widget of an unknown type', () => {
  it('opens the unsupported-widget panel, not the config panel, when it is selected', async () => {
    renderWorkspace();
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
    const placeholder = await screen.findByText('This widget needs a newer viewer');
    expect(screen.queryByText('Unsupported widget')).toBeNull();
    fireEvent.mouseDown(placeholder);
    expect(await screen.findByText('Unsupported widget')).toBeTruthy();
    expect(screen.getByText('Type: card-deck')).toBeTruthy();
    // Only the one panel: the config panel (headed by the widget's type) is not opened
    // beside it, so no option or data-source control can write into the placeholder.
    expect(document.querySelectorAll('aside')).toHaveLength(1);
    expect(screen.queryByText('unknown-widget')).toBeNull();
  });

  it('copies the stored widget as JSON, not the placeholder', async () => {
    renderWorkspace();
    openMenu(screen.getByRole('button', { name: /export/i }));
    fireEvent.click(await screen.findByRole('menuitem', { name: /copy json/i }));
    await waitFor(() => expect(clipboardWrites).toHaveLength(1));
    await act(async () => {});
    const exported = JSON.parse(clipboardWrites[0]) as { widgets: unknown[] };
    expect(JSON.stringify(exported.widgets[0])).toBe(JSON.stringify(newer));
  });
});
