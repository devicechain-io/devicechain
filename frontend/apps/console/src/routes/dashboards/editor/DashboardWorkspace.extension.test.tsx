// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An EXTERNAL widget package through the console's whole authoring flow, end to end, over
// the real workspace, canvas, panels, version drawer and publish action. Only the seam around
// the console is faked: the auth claims, the toast, the entity picker and the GraphQL
// transport (@devicechain/client's gql/subscribe), whose calls are recorded by operation.
//
// The package under test (@devicechain/host-widgets/fixtures/acme-tank) lives outside
// @devicechain/widgets, imports only the public SDK, and ships its own option text -- the
// console has no translation key for any word of it. The same fixture board is what the /dash
// viewer's test loads, so "authored here, viewed there" is one document, not two lookalikes.

import '@/i18n/config';
import { parseDashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { createHostWidgetRegistry } from '@devicechain/host-widgets';
import { ACME_TANK_ID, ACME_TANK_WIDGET_JSON, acmeTankWidget } from '@devicechain/host-widgets/fixtures/acme-tank';
import { defineWidget } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Every call that reaches the transport, by GraphQL operation name.
const net = vi.hoisted(() => ({
  ops: [] as Array<{ op: string; variables: Record<string, unknown> | undefined }>,
  opened: [] as Array<{ deviceToken: string; closed: boolean }>,
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    subscribe: (_area: string, _doc: unknown, variables: { deviceToken: string }) => {
      const entry = { deviceToken: variables.deviceToken, closed: false };
      net.opened.push(entry);
      return () => {
        entry.closed = true;
      };
    },
    gql: async (_area: string, doc: unknown, variables?: Record<string, unknown>) => {
      // The generated documents stringify to their operation text.
      const op = /(?:query|mutation|subscription)\s+(\w+)/.exec(String(doc))?.[1] ?? '?';
      net.ops.push({ op, variables });
      switch (op) {
        case 'UpdateDashboard':
          return { updateDashboard: { token: 'plant', updatedAt: '2026-10-11T10:00:00Z' } };
        case 'PublishDashboard':
          return {
            publishDashboard: { version: { version: 1 }, dashboard: { updatedAt: '2026-10-11T10:00:00Z' } },
          };
        case 'DashboardVersions':
          return { dashboardVersions: [] };
        case 'DashboardPublishedVersion':
          return { dashboard: { publishedVersion: null } };
        default:
          return {};
      }
    },
  };
});

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['dashboard:read', 'dashboard:write'] } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
// A device picker that takes a token typed into it.
vi.mock('./EntityPicker', () => ({
  EntityPicker: ({ value, onChange }: { value: string; onChange: (token: string) => void }) => (
    <input aria-label="entity picker" value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { DashboardWorkspace } from './DashboardWorkspace';

const resolver: DeviceResolver = { devicesForAnchor: async () => [], deviceExists: async () => true };

class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

let clipboard: string[];

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', NoopResizeObserver);
  net.ops.length = 0;
  net.opened.length = 0;
  clipboard = [];
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (text: string) => void clipboard.push(text) },
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });

// Radix menus open on pointerdown, not click.
const openMenu = (trigger: HTMLElement) =>
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: 'mouse' });

const ops = (name: string) => net.ops.filter((o) => o.op === name);

function mount(raw: unknown, registry = createHostWidgetRegistry([acmeTankWidget])) {
  const loaded = parseDashboardDefinition(raw, { catalog: registry.catalog });
  render(
    <MemoryRouter>
      <ConfirmProvider>
        <DashboardWorkspace
          token="plant"
          name="Plant"
          updatedAt="2026-10-11T09:00:00Z"
          loaded={loaded}
          resolver={resolver}
          registry={registry}
        />
      </ConfirmProvider>
    </MemoryRouter>,
  );
  return registry;
}

const emptyBoard = { title: 'Plant', widgets: [] };

// The Combobox trigger beside a field's label text, and its choices (a portal on the document).
async function pick(labelText: string, choiceText: string) {
  const trigger = screen.getByText(labelText).parentElement!.querySelector('button')!;
  fireEvent.click(trigger);
  const dialog = await screen.findByRole('dialog');
  fireEvent.click(within(dialog).getByText(choiceText));
}

describe('an external widget package through the console authoring flow', () => {
  it('is added from the menu, configured with package text, saved, published, exported and previewed', async () => {
    mount(emptyBoard);

    // ---- added from the Add menu, which lists it by the package's own title/category -----
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
    openMenu(screen.getByRole('button', { name: /add widget/i }));
    expect(await screen.findByText('Industrial')).toBeTruthy(); // the package's category heading
    fireEvent.click(await screen.findByTestId(`add-widget-${ACME_TANK_ID}`));

    // ---- configured: every label is the package's text, none is a console i18n key --------
    const panel = (await screen.findByText('Tank heading')).closest('aside')!;
    for (const text of ['Tank heading', 'Level unit', 'Capacity (litres)', 'Orientation', 'Shown above the level.']) {
      expect(within(panel).getByText(text), text).toBeTruthy();
    }
    // The enum is worded by the package too (its unset text falls to the host's generic one).
    // No raw translation key leaked anywhere in the panel.
    expect(panel.textContent).not.toMatch(/\bwidget[A-Z]\w+/);
    // Authored with the installed release, so there is no version notice to show.
    expect(screen.queryByTestId('widget-version-drift')).toBeNull();

    fireEvent.change(within(screen.getByTestId('widget-option-field-heading')).getByRole('textbox'), {
      target: { value: 'Main tank' },
    });
    const capacity = within(screen.getByTestId('widget-option-field-capacity')).getByRole('spinbutton');
    fireEvent.change(capacity, { target: { value: '5000' } });
    fireEvent.blur(capacity);

    // ---- bound to a device through a slot (the datasource block follows its policy) -------
    await pick('Kind', 'Device');
    fireEvent.change(screen.getByLabelText('entity picker'), { target: { value: 'tank-001' } });
    const measurements = screen.getByText('Measurements').parentElement!.querySelector('input')!;
    fireEvent.change(measurements, { target: { value: 'level' } });

    // ---- saved: the stored JSON carries the package's version reference -------------------
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }));
    await waitFor(() => expect(ops('UpdateDashboard')).toHaveLength(1));
    const request = ops('UpdateDashboard')[0].variables!.request as { definition: string };
    const stored = JSON.parse(request.definition) as {
      slots: Record<string, { defaultBinding?: unknown }>;
      widgets: Array<Record<string, unknown>>;
    };
    expect(stored.widgets).toHaveLength(1);
    const slotName = Object.keys(stored.slots)[0];
    expect(stored.slots[slotName].defaultBinding).toEqual({ kind: 'device', deviceToken: 'tank-001' });
    expect(normalized(stored.widgets[0], slotName)).toEqual(ACME_TANK_WIDGET_JSON);
    await flush();

    // ---- published: the version drawer enables Publish for a fully resolved board --------
    fireEvent.click(screen.getByRole('button', { name: /history/i }));
    const publish = await screen.findByRole('button', { name: /publish version/i });
    await waitFor(() => expect((publish as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(publish);
    await waitFor(() => expect(ops('PublishDashboard')).toHaveLength(1));
    expect(ops('PublishDashboard')[0].variables).toMatchObject({
      token: 'plant',
      expectedUpdatedAt: '2026-10-11T10:00:00Z',
    });
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('button', { name: /publish version/i })).toBeNull());

    // ---- exported: the export is the stored widget, version reference and all ------------
    openMenu(screen.getByRole('button', { name: /export/i }));
    fireEvent.click(await screen.findByRole('menuitem', { name: /copy json/i }));
    await waitFor(() => expect(clipboard).toHaveLength(1));
    const exported = JSON.parse(clipboard[0]) as { widgets: Array<Record<string, unknown>> };
    expect(normalized(exported.widgets[0], slotName)).toEqual(ACME_TANK_WIDGET_JSON);

    // ---- previewed on synthetic data: the backend is not touched ------------------------
    await flush();
    const before = net.ops.length;
    const openedBefore = net.opened.length;
    fireEvent.click(screen.getByRole('button', { name: /^preview$/i }));
    await flush();
    expect(net.ops.slice(before), 'preview called the backend').toEqual([]);
    expect(net.opened.slice(openedBefore), 'preview opened a live stream').toEqual([]);
  });
});

// The generated widget id and slot name are normalized so the authored document can be held
// to the shared fixture.
function normalized(widget: Record<string, unknown>, slotName: string) {
  const ds = widget.datasource as { slot: string };
  return { ...widget, id: 'tank-1', datasource: { ...ds, slot: ds.slot === slotName ? 'tank' : ds.slot } };
}

describe('a widget the console cannot resolve', () => {
  // An extension this host has no registration for, with a top-level field only its package
  // understands, and a built-in saved against a settings version this release does not read.
  const raw = {
    title: 'Plant',
    widgets: [
      {
        id: 'far',
        type: 'acme/far-widget',
        widget: { version: '3.0.0', optionsVersion: 1 },
        layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 3 } },
        options: { title: 'Far' },
        farSettings: { curve: [1, 2, 3] },
      },
      {
        id: 'g',
        type: 'gauge',
        widget: { version: '9.0.0', optionsVersion: 7 },
        layout: { base: { mode: 'grid' as const, col: 4, row: 0, cols: 4, rows: 3 } },
        options: { title: 'Temp', min: 0, max: 100 },
      },
    ],
  };

  it('is kept byte for byte by a draft save, and blocks publishing a new version', async () => {
    mount(raw);
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
    // An unrelated edit makes the draft dirty; the save must not touch the unresolved JSON.
    fireEvent.change(document.querySelector('#dash-title')!, { target: { value: 'Plant floor' } });
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }));
    await waitFor(() => expect(ops('UpdateDashboard')).toHaveLength(1));
    const saved = JSON.parse((ops('UpdateDashboard')[0].variables!.request as { definition: string }).definition) as {
      widgets: unknown[];
    };
    expect(JSON.stringify(saved.widgets)).toBe(JSON.stringify(raw.widgets));
    await flush();

    fireEvent.click(screen.getByRole('button', { name: /history/i }));
    const blocked = await screen.findByTestId('publish-blocked-unresolved');
    expect(blocked.textContent).toContain('acme/far-widget');
    expect(blocked.textContent).toContain('gauge');
    expect(blocked.textContent).toMatch(/package is not installed/);
    expect(blocked.textContent).toMatch(/different settings version/);
    const publish = screen.getByRole('button', { name: /publish version/i }) as HTMLButtonElement;
    expect(publish.disabled).toBe(true);
    fireEvent.click(publish);
    await flush();
    expect(ops('PublishDashboard')).toEqual([]);
  });
});

// An extension that DECLARES a write: its one button sends a command through whatever action
// seam the host hands it. Preview must hand it the offline one.
const VALVE_ID = 'acme/valve';
const valveWidget = defineWidget({
  descriptor: {
    id: VALVE_ID,
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Valve', category: 'Industrial' },
    channel: 'control',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { pageSize: { const: 5 } },
    options: {},
    defaults: {},
    interactions: { commandSend: true },
  },
  implementation: {
    kind: 'trusted-react',
    // The device is fixed rather than read from the command stream, so the test is about the
    // action seam alone (the transport mock answers the stream's history poll with nothing).
    component: ({ widget, actions }) => (
      <button
        type="button"
        data-testid={`valve-open-${widget.id}`}
        onClick={() => {
          if (actions?.sendCommand) void actions.sendCommand('valve-001', 'open').catch(() => {});
        }}
      >
        open
      </button>
    ),
  },
});

describe('the view-mode renderer and preview, with installed extensions', () => {
  const board = {
    title: 'Plant',
    slots: {
      tank: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'tank-001' } },
      valve: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'valve-001' } },
    },
    widgets: [
      ACME_TANK_WIDGET_JSON,
      {
        id: 'v1',
        type: VALVE_ID,
        widget: { version: '1.0.0', optionsVersion: 1 },
        layout: { base: { mode: 'grid' as const, col: 8, row: 0, cols: 4, rows: 2 } },
        datasource: { kind: 'slot', slot: 'valve' },
        options: {},
      },
    ],
  };

  it('draws an extension with the registry that parsed it, not as a placeholder', async () => {
    mount(board, createHostWidgetRegistry([acmeTankWidget, valveWidget]));
    // View mode (the workspace opens in it) renders through DashboardRenderer.
    expect(await screen.findByTestId('acme-tank-tank-1')).toBeTruthy();
    expect(await screen.findByTestId('valve-open-v1')).toBeTruthy();
  });

  it('sends a declared action live, and sends nothing from preview', async () => {
    mount(board, createHostWidgetRegistry([acmeTankWidget, valveWidget]));
    await flush();

    // Negative control: outside preview the same click reaches the transport, so the
    // assertion below can see a write if preview ever made one.
    fireEvent.click(await screen.findByTestId('valve-open-v1'));
    await waitFor(() => expect(ops('DashboardCreateCommand')).toHaveLength(1));
    expect(ops('DashboardCreateCommand')[0].variables).toMatchObject({ request: { deviceToken: 'valve-001', name: 'open' } });

    fireEvent.click(screen.getByRole('button', { name: /^preview$/i }));
    await flush();
    const before = net.ops.length;
    fireEvent.click(await screen.findByTestId('valve-open-v1'));
    await flush();
    expect(net.ops.slice(before), 'preview sent a live action').toEqual([]);
  });
});

describe('a widget authored with another release of its package', () => {
  it('still renders, and the editor says so without blocking anything', async () => {
    mount({
      title: 'Plant',
      widgets: [
        {
          ...ACME_TANK_WIDGET_JSON,
          id: 'w1',
          widget: { version: '1.1.0', optionsVersion: 1 },
          datasource: undefined,
        },
      ],
    });
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }));
    // Rendered by the installed implementation, not replaced by a placeholder.
    const tank = await screen.findByTestId('acme-tank-w1');
    fireEvent.pointerDown(tank);
    const notice = await screen.findByTestId('widget-version-drift');
    expect(notice.textContent).toContain('1.1.0');
    expect(notice.textContent).toContain('1.2.0');
    // Not a block: the drawer's publish is available for the (clean) board.
    fireEvent.click(screen.getByRole('button', { name: /history/i }));
    const publish = await screen.findByRole('button', { name: /publish version/i });
    await waitFor(() => expect((publish as HTMLButtonElement).disabled).toBe(false));
  });
});
