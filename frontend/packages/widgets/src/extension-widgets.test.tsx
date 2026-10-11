// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An extension widget through the whole runtime: parsed with the registry's catalog, rendered
// by the registry's implementation, bound through its declared policy, contained when it
// fails, and left completely inert when the registry cannot resolve it. The widgets here are
// test-only packages declared through the same public defineWidget a third party uses.

import {
  DashboardHub,
  parseDashboardDefinition,
  serializeDefinition,
  type DashboardDefinition,
  type WidgetActions,
  type WidgetDataSource,
} from '@devicechain/dashboards';
import {
  createWidgetRegistry,
  defineWidget,
  useWidgetCandidates,
  useWidgetSelect,
  type WidgetRegistry,
} from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Every network call the hub and the history seeder make, observable.
const net = vi.hoisted(() => ({
  opened: [] as Array<{ deviceToken: string; closed: boolean }>,
  history: [] as string[],
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
    gql: async (_area: string, _doc: unknown, variables?: { criteria?: { deviceToken?: string; intervalSeconds?: number } }) => {
      if (variables?.criteria?.intervalSeconds !== undefined) {
        net.history.push(String(variables.criteria.deviceToken));
        return {
          bucketedMeasurements: [{ name: 'level', bucketStart: '2026-01-01T00:00:00Z', avg: 5 }],
        };
      }
      return { alarms: { results: [], pagination: { totalRecords: 0 } } };
    },
  };
});

import { builtinWidgetDefinitions } from './builtin-registry';
import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);
beforeEach(() => {
  net.opened.length = 0;
  net.history.length = 0;
});

// ---- test-only extension packages ---------------------------------------------

const OPTIONS = {
  title: { kind: 'string', doc: 'Heading.' },
  unit: { kind: 'string', doc: 'Unit.' },
  samples: { kind: 'number', integer: true, min: 1, doc: 'Window.' },
} as const;

const tankWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.2.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'samples', min: 1 } },
    options: OPTIONS,
    defaults: { title: 'Tank' },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ widget, data }) => (
      <div data-testid={`tank-${widget.id}`}>
        {String(widget.options?.title)}|latest:{Object.keys(data.latest).length}|seeded:{data.samples.length}
      </div>
    ),
  },
});

// Throws while `fault.on`; recovers when the fault clears and the author presses Reset.
const fault = { on: true };
const boomWidget = defineWidget({
  descriptor: {
    id: 'acme/boom',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Boom' },
    channel: 'none',
    datasource: { mode: 'none' },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: () => {
      if (fault.on) throw new Error('boom: tank exploded');
      return <div data-testid="boom-ok">recovered</div>;
    },
  },
});

// An alarm-channel widget that declares alarm actions only. It shows what it was handed and
// surfaces a refusal itself, the way the built-in alarm table does.
const valveWidget = defineWidget({
  descriptor: {
    id: 'acme/valve',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Valve' },
    channel: 'alarm',
    datasource: { mode: 'optional', selectors: ['device', 'slot'] },
    binding: { pageSize: { const: 5 } },
    options: OPTIONS,
    defaults: {},
    interactions: { alarmActions: true },
  },
  implementation: {
    kind: 'trusted-react',
    component: function Valve({ actions }) {
      const [refusal, setRefusal] = useState('');
      return (
        <div>
          <span data-testid="valve-caps">
            ack:{typeof actions?.acknowledgeAlarm}|send:{typeof actions?.sendCommand}|cmd:{String(actions?.can('command:write'))}|alarm:
            {String(actions?.can('alarm:write'))}|select:{typeof useWidgetSelect()}
          </span>
          <button type="button" onClick={() => actions?.acknowledgeAlarm('a1').catch((e: Error) => setRefusal(e.message))}>
            ack
          </button>
          <span data-testid="valve-refusal">{refusal}</span>
        </div>
      );
    },
  },
});

// Declares no interaction at all: it must be handed none of the ambient host capabilities.
const peekWidget = defineWidget({
  descriptor: {
    id: 'acme/peek',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Peek' },
    channel: 'none',
    datasource: { mode: 'none' },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ actions }) => (
      <span data-testid="peek">
        actions:{typeof actions}|select:{typeof useWidgetSelect()}|candidates:{typeof useWidgetCandidates()}
      </span>
    ),
  },
});

const registry: WidgetRegistry = createWidgetRegistry([
  ...builtinWidgetDefinitions,
  tankWidget,
  boomWidget,
  valveWidget,
  peekWidget,
]);

// ---- fixtures ------------------------------------------------------------------

const box = (col: number) => ({ col, colSpan: 4, row: 0, rowSpan: 3, z: 0 });
const ref = { version: '1.2.0', optionsVersion: 1 };

const tankJson = (id: string, over: Record<string, unknown> = {}) => ({
  id,
  type: 'acme/tank-level',
  widget: ref,
  layout: { base: box(0) },
  datasource: { kind: 'slot', slot: 'tank', measurements: ['level'] },
  options: { title: id },
  ...over,
});

const raw = (widgets: unknown[]) => ({
  schemaVersion: 1,
  title: 'Ext',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { tank: { type: 'device' } },
  widgets,
});

const parse = (widgets: unknown[], reg: WidgetRegistry = registry) =>
  parseDashboardDefinition(raw(widgets), { catalog: reg.catalog });

const resolver = { devicesForAnchor: async () => [], deviceExists: async () => true };
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

function spySource() {
  const unsub = vi.fn();
  const source = {
    subscribeWidget: vi.fn((..._args: unknown[]) => unsub),
    subscribeAlarms: vi.fn((..._args: unknown[]) => unsub),
    subscribeCommands: vi.fn((..._args: unknown[]) => unsub),
    subscribeLocations: vi.fn((..._args: unknown[]) => unsub),
    isDatasourceAvailable: vi.fn(async () => true),
  };
  return { source, unsub, asSource: source as unknown as WidgetDataSource };
}

const calls = (s: ReturnType<typeof spySource>['source']) =>
  s.subscribeWidget.mock.calls.length +
  s.subscribeAlarms.mock.calls.length +
  s.subscribeCommands.mock.calls.length +
  s.subscribeLocations.mock.calls.length +
  s.isDatasourceAvailable.mock.calls.length;

const bound = { tank: { kind: 'device' as const, deviceToken: 'tank-001' } };

// ---- tests -----------------------------------------------------------------------

describe('an extension widget from custom JSON', () => {
  it('parses against the registry catalog and renders through its registered component', async () => {
    const def = parse([tankJson('t1')]);
    expect(def.widgets[0].type).toBe('acme/tank-level');
    const { source, asSource } = spySource();

    render(<DashboardRenderer definition={def} hub={asSource} registry={registry} bindings={bound} seedHistory={false} />);
    await flush();

    expect(screen.getByTestId('tank-t1').textContent).toBe('t1|latest:0|seeded:0');
    // Bound through its declared channel: one measurement subscription, for the bound device.
    expect(source.subscribeWidget).toHaveBeenCalledTimes(1);
    expect(source.subscribeWidget.mock.calls[0][0]).toMatchObject({ kind: 'device', deviceToken: 'tank-001' });
  });

  it('renders when the installed release differs from the one it was authored with', async () => {
    const def = parse([tankJson('t1', { widget: { version: '1.1.0', optionsVersion: 1 } })]);
    const { asSource } = spySource();
    render(<DashboardRenderer definition={def} hub={asSource} registry={registry} bindings={bound} seedHistory={false} />);
    await flush();
    expect(screen.getByTestId('tank-t1')).toBeTruthy();
  });
});

describe('a widget the registry cannot resolve', () => {
  const missing = tankJson('gone', { type: 'acme/not-installed', futureField: { a: [1, { b: 2 }] } });
  const mismatched = tankJson('old', { widget: { version: '0.9.0', optionsVersion: 7 }, futureField: 'kept' });
  const unversioned = (() => {
    const w = tankJson('bare');
    delete (w as Record<string, unknown>).widget;
    return w;
  })();

  it.each([
    ['missing', missing, /not installed/, 'This widget needs a newer viewer'],
    ['mismatched settings version', mismatched, /settings version 7.*version 1/, 'This widget cannot be shown here'],
    ['unversioned', unversioned, /without version metadata/, 'This widget cannot be shown here'],
  ])('%s: a placed placeholder with the reason, the stored JSON byte-equal, and nothing opened', async (_n, widget, reason, heading) => {
    const def = parse([widget]);
    const { source, asSource } = spySource();
    const actions = { acknowledgeAlarm: vi.fn(), clearAlarm: vi.fn(), sendCommand: vi.fn(), can: vi.fn(() => true) };

    // Even with the slot bound, a seed requested and every host capability wired.
    render(
      <DashboardRenderer
        definition={def}
        hub={asSource}
        registry={registry}
        bindings={bound}
        actions={actions as unknown as WidgetActions}
        select={vi.fn()}
        candidates={vi.fn(async () => [])}
        seedHistory
      />,
    );
    await flush();

    expect(screen.getByText(heading)).toBeTruthy();
    expect(screen.getByText(reason)).toBeTruthy();
    expect(calls(source)).toBe(0);
    expect(net.history).toEqual([]);
    expect(actions.can).not.toHaveBeenCalled();

    // Load → save: the widget is the stored bytes, version metadata and unknown fields included.
    const saved = JSON.parse(serializeDefinition(def)).widgets[0];
    expect(JSON.stringify(saved)).toBe(JSON.stringify(widget));
  });

  it('opens no stream on a REAL hub either', async () => {
    const def = parse([missing, mismatched]);
    const hub = new DashboardHub({ resolver });
    render(<DashboardRenderer definition={def} hub={hub} registry={registry} bindings={bound} seedHistory />);
    await flush();
    expect(hub.openStreamCount).toBe(0);
    expect(net.opened).toEqual([]);
    expect(net.history).toEqual([]);
    hub.disposeAll();
  });

  it('a registered extension handed to a registry that lacks it says so instead of rendering nothing', async () => {
    const def = parse([tankJson('t1')]); // parsed WITH the extension...
    const bare = createWidgetRegistry(builtinWidgetDefinitions); // ...rendered WITHOUT it
    const { source, asSource } = spySource();
    render(<DashboardRenderer definition={def} hub={asSource} registry={bare} bindings={bound} seedHistory={false} />);
    await flush();
    expect(screen.getByText(/not registered in this host/)).toBeTruthy();
    expect(calls(source)).toBe(0);
  });
});

describe('legacy unversioned built-ins', () => {
  it('still render through the registry, with no version metadata', async () => {
    const def = parse([
      { id: 'lbl', type: 'label', layout: { base: box(0) }, options: { text: 'Floor 3' } },
      { id: 'g', type: 'gauge', layout: { base: box(4) }, datasource: { kind: 'slot', slot: 'tank', measurements: ['level'] }, options: { title: 'G', min: 0, max: 10 } },
    ]);
    expect(def.widgets.map((w) => w.type)).toEqual(['label', 'gauge']);
    const { asSource } = spySource();
    render(<DashboardRenderer definition={def} hub={asSource} registry={registry} bindings={bound} seedHistory={false} />);
    await flush();
    expect(screen.getByText('Floor 3')).toBeTruthy();
    expect(JSON.stringify(JSON.parse(serializeDefinition(def)).widgets[0])).toBe(
      JSON.stringify({ id: 'lbl', type: 'label', layout: { base: box(0) }, options: { text: 'Floor 3' } }),
    );
  });
});

describe('failure containment', () => {
  it('a widget that throws shows a local diagnostic and its siblings keep rendering', async () => {
    fault.on = true;
    const def = parse([
      tankJson('t1'),
      { id: 'b', type: 'acme/boom', widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: box(4) } },
      { id: 'lbl', type: 'label', layout: { base: box(8) }, options: { text: 'still here' } },
    ]);
    const { asSource } = spySource();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});

    render(<DashboardRenderer definition={def} hub={asSource} registry={registry} bindings={bound} seedHistory={false} />);
    await flush();

    expect(screen.getByRole('alert').textContent).toContain('boom: tank exploded');
    expect(screen.getByTestId('tank-t1')).toBeTruthy();
    expect(screen.getByText('still here')).toBeTruthy();

    // The author fixes the cause and resets just that widget.
    fault.on = false;
    fireEvent.click(screen.getByRole('button', { name: 'Reset widget' }));
    await flush();
    expect(screen.getByTestId('boom-ok')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    consoleError.mockRestore();
  });

  it('a throwing widget releases its subscriptions', async () => {
    // A measurement widget that throws after subscribing must not leak its stream.
    const flaky = { on: false };
    const leaky = defineWidget({
      descriptor: { ...tankWidget.descriptor, id: 'acme/leaky', display: { title: 'Leaky' } },
      implementation: {
        kind: 'trusted-react',
        component: () => {
          if (flaky.on) throw new Error('late failure');
          return <div data-testid="leaky">ok</div>;
        },
      },
    });
    const reg = createWidgetRegistry([...builtinWidgetDefinitions, leaky]);
    const def = parse([{ ...tankJson('l1'), type: 'acme/leaky' }], reg);
    const hub = new DashboardHub({ resolver });
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    const view = render(<DashboardRenderer definition={def} hub={hub} registry={reg} bindings={bound} seedHistory={false} />);
    await flush();
    expect(hub.openStreamCount).toBe(1);

    flaky.on = true;
    view.rerender(
      <DashboardRenderer
        definition={{ ...def, widgets: [{ ...def.widgets[0], options: { title: 'changed' } }] } as DashboardDefinition}
        hub={hub}
        registry={reg}
        bindings={bound}
        seedHistory={false}
      />,
    );
    await flush();
    expect(screen.getByRole('alert')).toBeTruthy();
    expect(hub.openStreamCount).toBe(0); // the failed subtree unmounted and let go
    consoleError.mockRestore();
    hub.disposeAll();
  });
});

describe('subscriptions', () => {
  const sharedBoard = () =>
    parse([
      tankJson('t1'),
      tankJson('t2', { layout: { base: box(4) } }),
      { id: 'card', type: 'latest-card', layout: { base: box(8) }, datasource: { kind: 'slot', slot: 'tank', measurements: ['level'] }, options: { title: 'card' } },
    ]);

  it('widgets sharing a source share one telemetry stream, extension or built-in', async () => {
    const hub = new DashboardHub({ resolver });
    render(<DashboardRenderer definition={sharedBoard()} hub={hub} registry={registry} bindings={bound} seedHistory={false} />);
    await flush();
    // Three widgets, one device: ONE upstream subscription.
    expect(net.opened.map((o) => o.deviceToken)).toEqual(['tank-001']);
    expect(hub.openStreamCount).toBe(1);
    hub.disposeAll();
  });

  it('removal, rebinding and unmount release the stream', async () => {
    const hub = new DashboardHub({ resolver });
    const def = sharedBoard();
    const view = render(<DashboardRenderer definition={def} hub={hub} registry={registry} bindings={bound} seedHistory={false} />);
    await flush();
    expect(hub.openStreamCount).toBe(1);

    // Removing two of three widgets keeps the stream the third still reads.
    view.rerender(
      <DashboardRenderer definition={{ ...def, widgets: [def.widgets[2]] }} hub={hub} registry={registry} bindings={bound} seedHistory={false} />,
    );
    await flush();
    expect(hub.openStreamCount).toBe(1);
    expect(net.opened).toHaveLength(1);

    // Rebinding the slot moves the stream: the old one closes, one new one opens.
    view.rerender(
      <DashboardRenderer
        definition={{ ...def, widgets: [def.widgets[2]] }}
        hub={hub}
        registry={registry}
        bindings={{ tank: { kind: 'device', deviceToken: 'tank-002' } }}
        seedHistory={false}
      />,
    );
    await flush();
    expect(net.opened.map((o) => [o.deviceToken, o.closed])).toEqual([
      ['tank-001', true],
      ['tank-002', false],
    ]);
    expect(hub.openStreamCount).toBe(1);

    view.unmount();
    await flush();
    expect(hub.openStreamCount).toBe(0);
    expect(net.opened.every((o) => o.closed)).toBe(true);
  });

  it('an extension widget is seeded with history because its REGISTRATION is a measurement widget', async () => {
    const def = parse([
      tankJson('t1'),
      { id: 'v', type: 'acme/valve', widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: box(4) }, datasource: { kind: 'slot', slot: 'tank', measurements: [] } },
      tankJson('gone', { type: 'acme/not-installed' }),
    ]);
    const hub = new DashboardHub({ resolver });
    render(<DashboardRenderer definition={def} hub={hub} registry={registry} bindings={bound} seedHistory />);
    await flush();
    // One backfill, for the one measurement widget — not the alarm widget, not the placeholder.
    expect(net.history).toEqual(['tank-001']);
    expect(screen.getByTestId('tank-t1').textContent).toBe('t1|latest:1|seeded:1');
    hub.disposeAll();
  });
});

describe('interactions are narrowed to the declared ones', () => {
  const host = {
    acknowledgeAlarm: vi.fn(async () => {}),
    clearAlarm: vi.fn(async () => {}),
    sendCommand: vi.fn(async () => ({}) as never),
    can: vi.fn(() => true),
  };

  it('a widget declaring nothing is handed no actions, selection or candidates', async () => {
    const def = parse([{ id: 'p', type: 'acme/peek', widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: box(0) } }]);
    const { asSource } = spySource();
    render(
      <DashboardRenderer
        definition={def}
        hub={asSource}
        registry={registry}
        actions={host as unknown as WidgetActions}
        select={vi.fn()}
        candidates={vi.fn(async () => [])}
        seedHistory={false}
      />,
    );
    await flush();
    expect(screen.getByTestId('peek').textContent).toBe('actions:undefined|select:undefined|candidates:undefined');
  });

  it('a widget declaring alarm actions gets them, but not command send or the other write authority', async () => {
    const def = parse([{ id: 'v', type: 'acme/valve', widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: box(0) } }]);
    const { asSource } = spySource();
    render(
      <DashboardRenderer definition={def} hub={asSource} registry={registry} actions={host as unknown as WidgetActions} select={vi.fn()} seedHistory={false} />,
    );
    await flush();
    expect(screen.getByTestId('valve-caps').textContent).toBe('ack:function|send:undefined|cmd:false|alarm:true|select:undefined');
  });

  it('an action refusal stays visible: in the widget, and in a local diagnostic', async () => {
    const refusing = { ...host, acknowledgeAlarm: vi.fn(async () => { throw new Error('forbidden: alarm:write required'); }) };
    const def = parse([{ id: 'v', type: 'acme/valve', widget: { version: '1.0.0', optionsVersion: 1 }, layout: { base: box(0) } }]);
    const { asSource } = spySource();
    render(
      <DashboardRenderer definition={def} hub={asSource} registry={registry} actions={refusing as unknown as WidgetActions} seedHistory={false} />,
    );
    await flush();

    fireEvent.click(screen.getByRole('button', { name: 'ack' }));
    await flush();

    expect(refusing.acknowledgeAlarm).toHaveBeenCalledWith('a1');
    expect(screen.getByTestId('valve-refusal').textContent).toBe('forbidden: alarm:write required'); // the widget's own copy
    expect(screen.getByRole('status').textContent).toContain('forbidden: alarm:write required'); // the host's, beside it

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('built-ins keep what they declare: the alarm table still gets alarm actions', async () => {
    const def = parse([{ id: 'at', type: 'alarm-table', layout: { base: box(0) }, options: { title: 'Alarms' } }]);
    const { source, asSource } = spySource();
    render(<DashboardRenderer definition={def} hub={asSource} registry={registry} actions={host as unknown as WidgetActions} seedHistory={false} />);
    await flush();
    expect(source.subscribeAlarms).toHaveBeenCalledTimes(1);
    expect(source.subscribeAlarms.mock.calls[0][0]).toMatchObject({ pageSize: 50 });
  });
});
