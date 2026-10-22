// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reads the committed fixtures off disk; see the note in options.test.ts.
/// <reference types="node" />

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import type { AlarmRow, CommandRow, KnownWidgetInstance, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { isBuiltinWidget, parseDashboardDefinition, UNKNOWN_WIDGET_TYPE } from '@devicechain/dashboards';
import { cleanup, render as rtlRender } from '@testing-library/react';
import type { ComponentType, ReactElement } from 'react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

// jsdom has no canvas, so the ECharts wrapper is stubbed — as in widgets.test.tsx.
// The stub still receives the real option object the widget built from its options
// bag, so a chart's configuration is exercised even though nothing is painted.
vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  // Only the canvas wrapper is replaced (jsdom has no canvas); the pure option builders and
  // the colour theme stay real so a chart widget still builds the option it would draw.
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    EChart: ({ option }: { option: any }) => <div data-testid="echart" data-option={JSON.stringify(option)} />,
  };
});

import {
  type AggregateStreamState,
  type AlarmStreamState,
  type CommandStreamState,
  type LocationStreamState,
  type MeasurementStreamState,
} from '@devicechain/widget-sdk';
import { membersMode, presenceMode } from './binding-policy';
import { createBuiltinWidgetRegistry } from './builtin-registry';
import { CONTROL_STRINGS } from './command-control-strings';
import { FLEET_STRINGS } from './fleet-strings';
import { LEVEL_STRINGS } from './level-strings';
import { MapRuntimeProvider } from './map-runtime-context';
import { PRESENCE_STRINGS } from './presence-strings';
import { AGGREGATE_STRINGS } from './aggregate-strings';
import { loadAggregateWidgets } from './widgets/aggregate-lazy';
import { loadAlarmWidgets } from './widgets/alarm-lazy';
import { loadControlWidgets } from './widgets/control-lazy';
import { loadLevelWidgets } from './widgets/level-lazy';
import { loadPresenceWidgets } from './widgets/presence-lazy';
import { loadSelectionWidgets } from './widgets/selection-lazy';
import { loadTableWidget } from './widgets/table-lazy';

afterEach(cleanup);

// 🔴 The lazily loaded components (level and indicator, controls, aggregates, alarms, the
// selector and the table) load from lazy chunks, and until one arrives its registration renders
// NOTHING -- which every absence assertion below would read as clean. Loading them once here
// makes every later mount render synchronously; the positive
// level-family assertion further down is what fails if this ever stops being true.
beforeAll(() =>
  Promise.all([
    loadLevelWidgets(),
    loadPresenceWidgets(),
    loadControlWidgets(),
    loadAggregateWidgets(),
    loadAlarmWidgets(),
    loadSelectionWidgets(),
    loadTableWidget(),
  ]),
);

// ---- The render gate -----------------------------------------------------------
//
// Everything before this checks a document. This is the only step that runs the
// widgets, and therefore the only one that can observe an options bag being WRONG
// rather than merely being present: a bag is not a contract until something reads it.
//
// 🔴 "It rendered without throwing" is the same empty claim as "it parsed", and for
// the same reason — a widget's whole design is to degrade rather than fail. A card
// with no data shows an em dash, a table with none shows "No data yet", an image with
// no URL shows "No image URL", a command-button with no command shows "No command
// configured". Every one of those is a successful render of nothing. So the
// assertions below are about what the widget PUT ON THE SCREEN, and specifically
// about the placeholder states NOT appearing on a board that is supposed to be
// configured.
//
// The widgets are rendered through the registries as PURE components, which is the
// contract they are written to (see widget.ts): a function of (widget, data), so it
// renders identically from a live stream or a fixture. That is what makes this
// possible without standing up a hub.

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'sim-dashboards');

// 🔴 THIS GATE IS WIDGETLAB-ONLY, DELIBERATELY, AND THE SIBLING ONE IS NOT.
// sim-dashboard-fixture.test.ts walks the whole fixture directory, so every scenario's
// board is held to the parser and the option schemas. Rendering is different: the
// synthetic data below is built from widgetlab's own metrics and device tokens, and
// feeding a board data derived from the board is precisely the mistake this file was
// corrected for once already. Covering buildingpulse here means giving it its own
// externally-pinned data, which is real work and not what the publish gate needs.
// Naming the two boards keeps that limit visible instead of letting a directory walk
// imply coverage this file does not have.
const GALLERY = 'wl-gallery';
const STRESS = 'wl-stress';

// Every widget on these boards is a type this build renders; a placeholder for an
// unknown type would render nothing worth checking, so it fails here rather than being
// skipped (sim-dashboard-fixture.test.ts holds the same line).
function board(token: string) {
  const def = parseDashboardDefinition(JSON.parse(readFileSync(join(FIXTURES, `${token}.json`), 'utf8')));
  const widgets: KnownWidgetInstance[] = def.widgets.map((widget) => {
    if (widget.type === UNKNOWN_WIDGET_TYPE) throw new Error(`${token}: ${widget.id} has an unknown widget type`);
    if (!isBuiltinWidget(widget)) throw new Error(`${token}: ${widget.id} is not a built-in widget`);
    return widget;
  });
  return { ...def, widgets };
}

// The placeholders a widget can reach IN THIS HARNESS.
//
// 🔴 A hand-written list of failure strings overstates itself the moment one of them
// cannot fire. The first version listed seven; five were unreachable here — 'No data
// yet' and 'No alarms' need empty data the harness never supplies, and 'Data
// unavailable' / 'Device unavailable' live in ConnectedWidget, which this gate does
// not mount at all. It read as seven guarded states while guarding two.
//
// So the list is only the reachable ones, and the real work is done by the POSITIVE
// assertions below: a widget that renders its value, its rows, its series. Absence of
// a known failure string is a weak claim; presence of the expected output is not.
// 🔴 'Map runtime not configured' is listed because it is REACHABLE and must never appear
// on a board that is supposed to be configured. It is the state a map widget falls to when
// the host has not supplied MapLibre's worker URL — so this entry is what makes the harness
// wiring above load-bearing rather than decorative: remove the provider and this fails,
// instead of the board quietly rendering a notice nobody asserted against.
const EMPTY_STATES = [
  'No image URL',
  'No command configured',
  'Map runtime not configured',
  // The level family's explained refusals (a scale or the thresholds missing).
  LEVEL_STRINGS.en.setRange,
  LEVEL_STRINGS.en.badRange,
  LEVEL_STRINGS.en.setThresholds,
  // The command controls' explained refusals (no range, no parameter chosen).
  CONTROL_STRINGS.en.setRange,
  CONTROL_STRINGS.en.setParameter,
  // The aggregate widgets' explained states: on a board with data, none may be reached.
  AGGREGATE_STRINGS.en.loading,
  AGGREGATE_STRINGS.en.unsupported,
  AGGREGATE_STRINGS.en.noData,
  AGGREGATE_STRINGS.en.noCategories,
  AGGREGATE_STRINGS.en.pieNegative,
  AGGREGATE_STRINGS.en.pieNoShare,
  // The fleet widgets' explained states: a configured board has a group, access and members.
  FLEET_STRINGS.en.unbound,
  FLEET_STRINGS.en.forbidden,
  FLEET_STRINGS.en.unsupported,
  FLEET_STRINGS.en.noMembers,
  FLEET_STRINGS.en.noMeasurement,
  FLEET_STRINGS.en.failed,
  // The presence widgets' explained states: a configured board has a group, access and a status to read.
  PRESENCE_STRINGS.en.unsupported,
  PRESENCE_STRINGS.en.failed,
  PRESENCE_STRINGS.en.oneDevice,
];

const sample = (name: string, value: number): MeasurementSample => ({
  id: `${name}-1`,
  deviceToken: 'wl-sensor-01',
  eventType: 1,
  occurredTime: '2026-08-01T12:00:00Z',
  name,
  value,
  classifier: null,
});

// The metric vocabulary the widgetlab devices actually report.
//
// 🔴 FIXED, and deliberately NOT derived from the widget's own datasource. Building
// the data from what each widget asks for fabricates exactly the sample needed to
// make it look healthy: a card whose `options.measurement` is a typo would be handed
// a sample under the typo and render a value, when live it renders an em dash.
// primaryMeasurementName prefers `options.measurement` over the datasource, so that
// key is the single most likely place for a fixture to be wrong, and reading it back
// out of the fixture to build the input is the self-referential shape this whole arc
// has been correcting.
const REPORTED_METRICS = ['temperature', 'humidity', 'pressure', 'battery'];

const SAMPLE_VALUE = 21.5;

function measurementData(): MeasurementStreamState {
  const latest: Record<string, MeasurementSample> = {};
  for (const name of REPORTED_METRICS) latest[name] = sample(name, SAMPLE_VALUE);
  return {
    latest,
    samples: REPORTED_METRICS.map((name) => sample(name, SAMPLE_VALUE)),
    error: null,
  };
}

// What the aggregate read returned over the hour before the samples above. Distinct per measurement
// and per statistic, so a card that read the wrong field or the wrong name shows the wrong figure.
const AGGREGATE_DATA: AggregateStreamState = {
  aggregates: {
    status: 'ready',
    startTime: '2026-08-01T11:00:00Z',
    endTime: '2026-08-01T12:00:00Z',
    spanSeconds: 3600,
    byName: {
      temperature: { count: 40, min: 12, max: 31.5, sum: 800, avg: 20 },
      humidity: { count: 20, min: 40, max: 70, sum: 1100, avg: 55 },
      battery: { count: 10, min: 60, max: 90, sum: 750, avg: 75 },
    },
  },
  latest: measurementData().latest,
  error: null,
};

// A fleet of three machines for the widgets that read member by member: each reports its OWN
// temperature (so a row that showed another device's value shows a wrong number), one never reports
// the battery, and the third device is the one the table's colour test looks at.
const FLEET_DATA: MeasurementStreamState = {
  latest: {},
  samples: [],
  error: null,
  members: {
    status: 'ready',
    total: 3,
    truncated: false,
    members: [
      { token: 'wl-sensor-01', name: 'Sensor 1', temperature: 21.5, humidity: 40, battery: 88 },
      { token: 'wl-sensor-02', name: 'Sensor 2', temperature: 29, humidity: 52, battery: null },
      { token: 'wl-sensor-03', name: 'Sensor 3', temperature: 33, humidity: 61, battery: 71 },
    ].map(({ token, name, ...readings }) => {
      const latest: Record<string, MeasurementSample> = {};
      for (const [metric, value] of Object.entries(readings)) {
        if (value !== null) latest[metric] = { ...sample(metric, value), deviceToken: token };
      }
      return { token, name, latest, seed: 'ready' as const, observed: Object.keys(latest) };
    }),
  },
};

// What device-state says of the gallery's sensors, for the presence widgets: two online (one seen a
// minute ago, one an hour ago), one offline, and a fourth that has never sent anything. The status
// card is bound to ONE sensor, so it is given the first; the doughnut, the zone's four.
const PRESENCE_MEMBERS = [
  { token: 'wl-sensor-01', presence: { status: 'ready' as const, active: true, lastActivityTime: '2026-08-01T11:59:00Z', inactivityTimeout: 600 } },
  { token: 'wl-sensor-02', presence: { status: 'ready' as const, active: true, lastActivityTime: '2026-08-01T11:00:00Z', inactivityTimeout: 600 } },
  { token: 'wl-sensor-03', presence: { status: 'ready' as const, active: false, lastActivityTime: '2026-07-31T12:00:00Z', inactivityTimeout: 600 } },
  { token: 'wl-sensor-04', presence: { status: 'unknown' as const } },
].map(({ token, presence }) => ({ token, name: token, latest: {}, seed: 'ready' as const, observed: [], presence }));

function presenceData(widget: KnownWidgetInstance): MeasurementStreamState {
  const members = widget.type === 'device-status' ? PRESENCE_MEMBERS.slice(0, 1) : PRESENCE_MEMBERS;
  return { latest: {}, samples: [], error: null, members: { status: 'ready', members, total: members.length, truncated: false } };
}

const alarmRow: AlarmRow = {
  token: 'alarm-1',
  originatorType: 'device',
  originatorToken: 'wl-sensor-01',
  alarmKey: 'wl-over-temp',
  metricKey: 'temperature',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'MAJOR',
  raisedTime: '2026-08-01T12:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 31.5,
};

// A completed command round trip, as a RENDER INPUT: this asserts the widget draws
// an acked command, and says nothing about whether anything acks one.
//
// It used to say something it should not have. While the widgetlab devices were
// HTTP-ingress-only, SUCCESSFUL existed nowhere but here — the one place the round
// trip completed was a literal in a test, and a reader could take a green gate for
// evidence of a working control channel. The scenario now attaches a command
// receiver per device (SimManifest.CommandFarEnd), so this shape is one the run
// genuinely reaches.
//
// respondedTime is set, not null: the response IS what makes a command successful,
// so a SUCCESSFUL row without one is a state the platform never produces, and a
// widget reading it would render a blank against a fixture nobody would question.
const commandRow: CommandRow = {
  token: 'cmd-1',
  name: 'setSetpoint',
  status: 'SUCCESSFUL',
  payload: '{"target":22}',
  responsePayload: null,
  error: null,
  queuedTime: '2026-08-01T12:00:00Z',
  sentTime: '2026-08-01T12:00:01Z',
  respondedTime: '2026-08-01T12:00:02Z',
};

const alarmData: AlarmStreamState = { alarms: [alarmRow], total: 1, loading: false, error: null };
// A located device, carrying a position AND leaving two optionals unreported —
// because a fixture where every field is present cannot show that an absent one is
// omitted rather than rendered as a zero.
const locationData: LocationStreamState = {
  locations: [
    {
      id: 'wl-loc-1',
      deviceToken: 'wl-sensor-01',
      latitude: 33.749,
      longitude: -84.388,
      elevation: 320.5,
      accuracy: 4.2,
      speed: null,
      heading: null,
      occurredTime: '2026-08-09T12:00:00Z',
    },
  ],
  deviceTokens: ['wl-sensor-01'],
  forbidden: false,
  loading: false,
  error: null,
};

const commandData: CommandStreamState = {
  deviceToken: 'wl-sensor-01',
  commands: [commandRow],
  total: 1,
  loading: false,
  error: null,
};

// 🔴 THE HOST WIRING IS PART OF THE HARNESS NOW, and leaving it out was a real coverage
// loss rather than a cosmetic one. wl-gallery contains a map widget; @devicechain/widgets
// takes its MapLibre worker URL from the host, so a bare render makes that widget short-
// circuit to "Map runtime not configured" and never touch its location data. Every
// assertion below would still have passed, because the notice is not in EMPTY_STATES and
// the positive assertions do not name the map — the board would simply have stopped being
// tested, quietly, which is precisely what this file exists to prevent.
//
// The notice IS in EMPTY_STATES below as well, so if this wiring is ever removed the board
// fails loudly instead of going dark again.
const TEST_MAP_RUNTIME = { workerUrl: 'blob:widgetlab/maplibre-worker.mjs' };

function render(ui: ReactElement) {
  return rtlRender(<MapRuntimeProvider runtime={TEST_MAP_RUNTIME}>{ui}</MapRuntimeProvider>);
}

// renderWidget runs one widget the way the renderer does: the component its registration
// carries, fed the data its registered CHANNEL binds.
const registry = createBuiltinWidgetRegistry();

function renderWidget(widget: KnownWidgetInstance) {
  const Component = registry.implementation(widget.type)?.component as ComponentType<{
    widget: KnownWidgetInstance;
    data: unknown;
  }>;
  switch (registry.descriptor(widget.type)?.channel) {
    case 'alarm':
      return render(<Component widget={widget} data={alarmData} />);
    case 'control':
      return render(<Component widget={widget} data={commandData} />);
    case 'location':
      return render(<Component widget={widget} data={locationData} />);
    case 'measurement':
      // The descriptor decides whether a measurement widget reads member by member, or reads device
      // connectivity, as it does live.
      if (presenceMode(registry.descriptor(widget.type)!)) return render(<Component widget={widget} data={presenceData(widget)} />);
      return render(
        <Component
          widget={widget}
          data={membersMode(registry.descriptor(widget.type)!, widget as unknown as WidgetInstance) ? FLEET_DATA : measurementData()}
        />,
      );
    case 'aggregate':
      return render(<Component widget={widget} data={AGGREGATE_DATA} />);
    case 'selection':
    case 'none':
      return render(<Component widget={widget} data={null} />);
    // 🔴 An unregistered widget or an unlisted channel must fail loudly here, not fall through
    // to some default data shape: a case missing for a new channel would otherwise render the
    // widget with the wrong snapshot and still pass.
    default:
      throw new Error(`no channel case for widget type ${widget.type}`);
  }
}

describe('rendering the widgetlab boards', () => {
  // The gallery is the catalog: it must carry an instance of EVERY registration in the
  // built-in registry (not just every id in a list), and render each through the component
  // that registration carries -- so a built-in registered without a fixture, or a fixture
  // for a widget the registry does not hold, is a failure here.
  it('gives the gallery a rendering instance of every built-in registration', () => {
    const registered = registry.list().map((d) => d.descriptor.id);
    expect(registered.length).toBeGreaterThan(0);
    const widgets = board(GALLERY).widgets;
    for (const id of registered) {
      const instances = widgets.filter((w) => w.type === id);
      expect(instances.length, `the gallery has no ${id} widget`).toBeGreaterThan(0);
      expect(() => renderWidget(instances[0]), `${id} threw while rendering`).not.toThrow();
      cleanup();
    }
    for (const w of widgets) {
      expect(registered, `${w.id}: gallery type ${w.type} is not a registered built-in`).toContain(w.type);
    }
  });

  for (const token of [GALLERY, STRESS]) {
    describe(token, () => {
      it('renders every widget', () => {
        const widgets = board(token).widgets;
        expect(widgets.length).toBeGreaterThan(0);
        for (const widget of widgets) {
          expect(() => renderWidget(widget), `${widget.id} (${widget.type}) threw while rendering`).not.toThrow();
          cleanup();
        }
      });

      it('renders no widget into a placeholder it could reach here', () => {
        for (const widget of board(token).widgets) {
          const { container } = renderWidget(widget);
          const text = container.textContent ?? '';
          for (const placeholder of EMPTY_STATES) {
            expect(
              text,
              `${widget.id} (${widget.type}) rendered "${placeholder}" — it is on a configured ` +
                `board with data, so its options did not reach it`,
            ).not.toContain(placeholder);
          }
          cleanup();
        }
      });
    });
  }
});

// ---- What the widget actually DREW ---------------------------------------------
//
// 🔑 The real gate. Absence of a known failure string is weak: a card can render its
// title and no value, a chart can plot an empty series, an alarm table can render its
// headers over an empty body — all without producing any placeholder text at all, and
// all of them were green before these assertions existed.
//
// These run on the GALLERY only. The stress board is fed data that makes it look
// healthy (the harness has no pathological generator), so asserting it drew anything
// in particular would be asserting the opposite of its purpose; what it is held to is
// that it renders and that its options reach the renderer.

describe('the gallery widgets draw their data', () => {
  function galleryWidgets(type: string): KnownWidgetInstance[] {
    return board(GALLERY).widgets.filter((w) => w.type === type);
  }

  it('shows a value on every single-value widget', () => {
    // primaryMeasurementName prefers options.measurement, so a card or gauge naming a
    // metric the devices do not report renders an em dash with no value at all — the
    // most likely fixture regression there is, and one no schema check can see
    // because a typo is a perfectly legal string.
    let checked = 0;
    for (const widget of galleryWidgets('latest-card')) {
      checked++;
      const { container } = renderWidget(widget);
      expect(
        container.textContent,
        `${widget.id} rendered no value — its measurement is probably not one the devices report`,
      ).toContain(String(SAMPLE_VALUE.toFixed(1)));
      cleanup();
    }
    expect(checked, 'the gallery has no latest-card').toBeGreaterThan(0);
  });

  it('draws a reading on every level and indicator widget', () => {
    // Each of the five marks what it drew: a fill fraction, a lamp lit or dark, a count of lit bars.
    // An explained empty state (no scale, no data) carries none of them.
    const DREW = '[data-fill], [data-lit], [data-bars-lit]';
    let checked = 0;
    for (const type of ['linear-gauge', 'level-gauge', 'battery', 'status-light', 'signal-bars']) {
      for (const widget of galleryWidgets(type)) {
        checked++;
        const { container } = renderWidget(widget);
        expect(container.querySelector('[data-level-empty]')?.textContent, `${widget.id} drew an empty state`).toBeUndefined();
        expect(container.querySelector(DREW), `${widget.id} (${type}) drew no reading`).not.toBeNull();
        cleanup();
      }
    }
    expect(checked, 'the gallery has no level or indicator widget').toBeGreaterThanOrEqual(5);
  });

  it('draws a reading on every radial widget', () => {
    // The ring marks its fill; the compass and the gauge have only their readout to show for a
    // reading (the dial is canvas). An empty state renders no readout at all, and the gauge's
    // em dash for a missing sample is checked by text.
    let checked = 0;
    for (const type of ['compass', 'progress-ring', 'gauge']) {
      for (const widget of galleryWidgets(type)) {
        checked++;
        const { container } = renderWidget(widget);
        expect(container.querySelector('[data-level-empty]')?.textContent, `${widget.id} drew an empty state`).toBeUndefined();
        const readout = container.querySelector('[data-level-readout]');
        expect(readout, `${widget.id} (${type}) drew no readout`).not.toBeNull();
        expect(readout!.textContent, `${widget.id} (${type}) read no value`).toMatch(/\d/);
        if (type === 'progress-ring') expect(container.querySelector('[data-fill]'), `${widget.id} drew no fill`).not.toBeNull();
        cleanup();
      }
    }
    expect(checked, 'the gallery has no radial widget').toBeGreaterThanOrEqual(4);
  });

  it('draws a working command control on the gallery, bound to the published command', () => {
    // A control the schema refuses draws a role=alert notice instead of an input; one that works
    // carries its slider, or its switch (or, with no power command in the history, the explicit
    // On / Off choice), and the setpoint slider reads the round-trip row above.
    let checked = 0;
    for (const [type, role] of [
      ['command-slider', 'input[type="range"]'],
      ['command-toggle', 'button[role="switch"], [data-toggle-choose] button'],
    ] as const) {
      for (const widget of galleryWidgets(type)) {
        checked++;
        const { container } = renderWidget(widget);
        expect(container.querySelector('[data-control-empty]')?.textContent, `${widget.id} refused to draw`).toBeUndefined();
        expect(container.querySelector(role), `${widget.id} (${type}) drew no ${role}`).not.toBeNull();
        if (type === 'command-slider') {
          expect((container.querySelector('input[type="range"]') as HTMLInputElement).value).toBe('22');
          expect(container.querySelector('[data-last-sent]')?.textContent).toMatch(/Last sent.*22.*Delivered/);
        }
        cleanup();
      }
    }
    expect(checked, 'the gallery has no command slider or toggle').toBeGreaterThanOrEqual(4);
  });

  it('shows the statistic each stat card is set to, over the span it says', () => {
    // The figures are the ones AGGREGATE_DATA gives each measurement and field (or the latest
    // reading), so a card that read another field, another measurement, or a bucket average shows
    // a different number. The caption names the statistic and the span.
    const EXPECT: Record<string, { figure: string; caption: string }> = {
      'wl-stat-avg': { figure: '20', caption: 'Average · 1h' },
      'wl-stat-max': { figure: '31.5', caption: 'Maximum · 1h' },
      'wl-stat-min': { figure: '12', caption: 'Minimum · 1h' },
      'wl-stat-count': { figure: '40', caption: 'Readings · 1h' },
      'wl-stat-sum': { figure: '1100', caption: 'Sum · 1h' },
      'wl-stat-last': { figure: '21.5', caption: 'Latest' },
      'wl-stat-pinned': { figure: '20', caption: 'Average · 1h' },
    };
    let checked = 0;
    for (const widget of galleryWidgets('stat-card')) {
      const want = EXPECT[widget.id];
      if (!want) continue;
      checked++;
      const { container } = renderWidget(widget);
      expect(container.querySelector('[data-level-readout]')?.textContent, `${widget.id} shows another figure`).toContain(want.figure);
      expect(container.querySelector('[data-stat-caption]')?.textContent, `${widget.id} captions another statistic or span`).toBe(want.caption);
      cleanup();
    }
    expect(checked, 'the gallery lost a stat card').toBe(Object.keys(EXPECT).length);
  });

  it('gives the bar and pie charts a category per measurement, valued by its latest reading', () => {
    let checked = 0;
    for (const type of ['bar-chart', 'pie-chart']) {
      // A bar chart set to one bar per device draws devices, not measurements (the test below).
      for (const widget of galleryWidgets(type).filter((w) => w.options?.groupBy !== 'device')) {
        checked++;
        const { container } = renderWidget(widget);
        const names = widget.datasource && 'measurements' in widget.datasource ? widget.datasource.measurements : [];
        // A bar's value rides on its group; a pie wedge's share does (every category reads the same sample).
        const drawn = [...container.querySelectorAll(type === 'bar-chart' ? '[data-bar]' : '[data-slice]')];
        expect(drawn.map((el) => el.getAttribute(type === 'bar-chart' ? 'data-bar' : 'data-slice')).sort(), `${widget.id} lost a category`).toEqual([...names].sort());
        for (const el of drawn) {
          if (type === 'bar-chart') expect(Number(el.getAttribute('data-value'))).toBe(SAMPLE_VALUE);
          else expect(Number(el.getAttribute('data-fraction'))).toBeCloseTo(1 / names.length, 6);
        }
        cleanup();
      }
    }
    expect(checked, 'the gallery lost a bar or pie chart').toBeGreaterThanOrEqual(4);
  });

  it('draws the fleet: a row per device and a column per measurement, each cell that device�s own reading', () => {
    // FLEET_DATA gives every machine its OWN temperature, so a row that showed another machine's
    // value, or one value for the whole column, shows a wrong number.
    const EXPECT: Record<string, string> = { 'wl-sensor-01': '21.5', 'wl-sensor-02': '29', 'wl-sensor-03': '33' };
    let checked = 0;
    for (const widget of galleryWidgets('fleet-table')) {
      checked++;
      const { container } = renderWidget(widget);
      expect(container.querySelector('[data-level-empty]')?.textContent, `${widget.id} drew an empty state`).toBeUndefined();
      const names = widget.datasource && 'measurements' in widget.datasource ? widget.datasource.measurements : [];
      expect([...container.querySelectorAll('thead th')].length, `${widget.id} lost a column`).toBe(names.length + 1);
      expect(container.querySelectorAll('tbody tr'), `${widget.id} lost a device`).toHaveLength(3);
      for (const [device, temperature] of Object.entries(EXPECT)) {
        const td = container.querySelector(`tr[data-device="${device}"] td[data-measurement="temperature"]`);
        expect(td?.textContent, `${widget.id}: ${device} shows another device's temperature`).toContain(temperature);
      }
      cleanup();
    }
    expect(checked, 'the gallery lost a fleet table').toBeGreaterThanOrEqual(2);
  });

  it('draws the connectivity widgets: the sensor online, and the zone as two online, one offline and one never seen', () => {
    let checked = 0;
    for (const widget of galleryWidgets('device-status')) {
      checked++;
      const { container } = renderWidget(widget);
      expect(container.querySelector('[data-device-status]')?.getAttribute('data-device-status'), `${widget.id} lost the status`).toBe('online');
      expect(container.querySelector('[data-device-status-caption]')?.textContent, `${widget.id} lost the last seen`).toContain('Last seen');
      cleanup();
    }
    expect(checked, 'the gallery lost a device status card').toBeGreaterThanOrEqual(1);
    for (const widget of galleryWidgets('online-doughnut')) {
      checked++;
      const { container } = renderWidget(widget);
      const ring = container.querySelector('[data-online-doughnut]');
      expect([ring?.getAttribute('data-online'), ring?.getAttribute('data-offline'), ring?.getAttribute('data-no-status')], `${widget.id} miscounted`).toEqual(['2', '1', '1']);
      cleanup();
    }
    expect(checked, 'the gallery lost an online doughnut').toBeGreaterThanOrEqual(2);
  });

  it('draws one bar per device on the per-device bar chart', () => {
    let checked = 0;
    for (const widget of galleryWidgets('bar-chart').filter((w) => w.options?.groupBy === 'device')) {
      checked++;
      const { container } = renderWidget(widget);
      const drawn = [...container.querySelectorAll('[data-bar]')].map((el) => [el.getAttribute('data-bar'), Number(el.getAttribute('data-value'))]);
      expect(drawn.sort(), `${widget.id} lost a device`).toEqual([['Sensor 1', 21.5], ['Sensor 2', 29], ['Sensor 3', 33]]);
      cleanup();
    }
    expect(checked, 'the gallery lost the per-device bar chart').toBe(1);
  });

  it('puts a reading on the gauge rather than an empty dial', () => {
    let checked = 0;
    for (const widget of galleryWidgets('gauge')) {
      checked++;
      const { container } = renderWidget(widget);
      const option = JSON.parse(
        container.querySelector('[data-testid="echart"]')?.getAttribute('data-option') ?? '{}',
      );
      expect(
        option.series?.[0]?.data?.[0]?.value,
        `${widget.id}'s needle has no reading — with no sample the gauge rests at its minimum`,
      ).toBe(SAMPLE_VALUE);
      cleanup();
    }
    expect(checked, 'the gallery has no gauge').toBeGreaterThan(0);
  });

  it('plots a series per measurement the chart selects', () => {
    let checked = 0;
    for (const widget of galleryWidgets('timeseries-chart')) {
      checked++;
      const { container } = renderWidget(widget);
      const option = JSON.parse(
        container.querySelector('[data-testid="echart"]')?.getAttribute('data-option') ?? '{}',
      );
      const selected =
        widget.datasource && 'measurements' in widget.datasource ? widget.datasource.measurements : [];
      expect(option.series?.length, `${widget.id} plots no series at all`).toBe(selected.length);
      for (const series of option.series ?? []) {
        expect(series.data?.length, `${widget.id}'s ${series.name} series has no points`).toBeGreaterThan(0);
      }
      cleanup();
    }
    expect(checked, 'the gallery has no chart').toBeGreaterThan(0);
  });

  it('gives the table a row per reported measurement', () => {
    let checked = 0;
    for (const widget of galleryWidgets('table')) {
      checked++;
      const { container } = renderWidget(widget);
      // One <tbody> row per measurement. Headers alone render happily over an empty
      // body and produce no placeholder, because "No data yet" only appears when the
      // latest map is empty — not when the rows were dropped.
      const rows = container.querySelectorAll('tbody tr');
      expect(rows.length, `${widget.id} rendered headers with no rows`).toBe(REPORTED_METRICS.length);
      cleanup();
    }
    expect(checked, 'the gallery has no table').toBeGreaterThan(0);
  });

  it('gives the alarm table a row carrying the alarm', () => {
    let checked = 0;
    for (const widget of galleryWidgets('alarm-table')) {
      checked++;
      const { container } = renderWidget(widget);
      const rows = container.querySelectorAll('tbody tr');
      expect(rows.length, `${widget.id} rendered headers with no alarm rows`).toBe(1);
      // The row must carry the alarm's own content, not just exist.
      expect(container.textContent, `${widget.id}'s row does not name the alarm`).toContain(alarmRow.alarmKey);
      expect(container.textContent, `${widget.id}'s row does not name the originator`).toContain(
        alarmRow.originatorToken as string,
      );
      cleanup();
    }
    expect(checked, 'the gallery has no alarm-table').toBeGreaterThan(0);
  });

  it('shows the alarm total on the count', () => {
    let checked = 0;
    for (const widget of galleryWidgets('alarm-count')) {
      checked++;
      const { container } = renderWidget(widget);
      expect(container.textContent, `${widget.id} shows no total`).toContain(String(alarmData.total));
      cleanup();
    }
    expect(checked, 'the gallery has no alarm-count').toBeGreaterThan(0);
  });
});

// ---- What the options actually produced ---------------------------------------
//
// The checks above prove a widget showed something. These prove it showed what its
// options asked for — the difference between a bag that is legal and a bag that is
// connected to anything.

describe('the widgetlab options reach the rendered output', () => {
  function widgetsOf(token: string, type: string): KnownWidgetInstance[] {
    return board(token).widgets.filter((w) => w.type === type);
  }

  it('renders every title a widget authored', () => {
    let checked = 0;
    for (const token of [GALLERY, STRESS]) {
      for (const widget of board(token).widgets) {
        const title = widget.options?.title;
        if (typeof title !== 'string' || title === '') continue;
        if (registry.descriptor(widget.type)?.channel === 'selection') continue;
        checked++;
        const { container } = renderWidget(widget);
        expect(container.textContent, `${widget.id}'s title never reached the DOM`).toContain(title);
        cleanup();
      }
    }
    expect(checked, 'no widget authored a title, so this asserted nothing').toBeGreaterThan(0);
  });

  it('renders the label text and honours its alignment', () => {
    for (const token of [GALLERY, STRESS]) {
      for (const widget of widgetsOf(token, 'label')) {
        const { container } = renderWidget(widget);
        expect(container.textContent).toContain(widget.options?.text as string);
        cleanup();
      }
    }
  });

  it('gives the image the source it authored', () => {
    let checked = 0;
    for (const token of [GALLERY, STRESS]) {
      for (const widget of widgetsOf(token, 'image')) {
        checked++;
        const { container } = renderWidget(widget);
        const img = container.querySelector('img');
        expect(img, `${widget.id} rendered no <img> at all`).not.toBeNull();
        expect(img?.getAttribute('src')).toBe(widget.options?.url);
        // 🔴 Pinned to the DESIGN, not to the option: a data: URI, never a network
        // fetch. A board that reaches out fails closed behind an air-gapped deploy's
        // egress policy and renders the broken-image state — which reads as a broken
        // WIDGET rather than a blocked request, on the one board whose job is showing
        // what a widget looks like. Nothing else enforces this.
        expect(
          img?.getAttribute('src') ?? '',
          `${widget.id} loads its image over the network; behind an egress policy it would ` +
            `render as a broken widget`,
        ).toMatch(/^data:/);
        // The alt text is what a screen reader reads, and the only thing shown when
        // the source fails to decode — which is precisely the stress board's case.
        expect(img?.getAttribute('alt')).toBe(widget.options?.alt ?? '');
        cleanup();
      }
    }
    expect(checked, 'no image widget was rendered').toBeGreaterThan(0);
  });

  it('builds the gauge scale from the options rather than the default', () => {
    let checked = 0;
    for (const token of [GALLERY, STRESS]) {
      for (const widget of widgetsOf(token, 'gauge')) {
        checked++;
        const { container } = renderWidget(widget);
        const option = container.querySelector('[data-testid="echart"]')?.getAttribute('data-option');
        expect(option, `${widget.id} rendered no chart`).toBeTruthy();
        const parsed = JSON.parse(option as string);
        // A gauge with no min/max silently falls back to 0..100, which looks like a
        // working gauge on a scale nobody chose.
        expect(parsed.series?.[0]?.min, `${widget.id}'s min did not reach the chart`).toBe(
          widget.options?.min,
        );
        expect(parsed.series?.[0]?.max, `${widget.id}'s max did not reach the chart`).toBe(
          widget.options?.max,
        );
        cleanup();
      }
    }
    expect(checked, 'no gauge was rendered').toBeGreaterThan(0);
  });

  it('renders the command button with the label its options name', () => {
    let checked = 0;
    for (const widget of widgetsOf(GALLERY, 'command-button')) {
      checked++;
      const { container } = renderWidget(widget);
      const label = (widget.options?.commandLabel ?? widget.options?.commandName) as string;
      expect(container.textContent, `${widget.id} never rendered its send label`).toContain(label);
      // The baked parameter schema drives the form. A widget with no schema — or one
      // that failed to parse — renders a bare Send button, which looks like a
      // no-argument command rather than a broken one, so its absence has to be a
      // failure rather than an empty loop. Two parameters minimum, because the
      // console's form branches on type and one leaves most of the widget
      // unexercised; that is the reason this command was given two in the first place.
      const raw = widget.options?.parameterSchema;
      expect(typeof raw, `${widget.id} bakes no parameterSchema, so it renders a bare Send button`).toBe(
        'string',
      );
      const schema = JSON.parse(raw as string) as Array<{ name: string; dataType?: string; kind?: string }>;
      expect(schema.length, `${widget.id}'s schema declares ${schema.length} parameters; the form ` +
        `needs at least 2 for its type branches to be exercised`).toBeGreaterThanOrEqual(2);
      expect(
        new Set(schema.map((p) => p.dataType)).size,
        `${widget.id}'s parameters all share a data type, so the form renders one kind of input`,
      ).toBeGreaterThan(1);
      // 🔴 Every parameter must be a SCALAR. A structured (OBJECT) parameter is not
      // form-editable, and a REQUIRED one blocks every send — the widget renders
      // "required structured parameter — not supported here" and the button can never
      // do anything. It produces no placeholder text and the per-parameter check
      // below still matches, because the parameter's NAME appears inside that very
      // hint: a form that can never send, indistinguishable from a working one.
      for (const param of schema) {
        expect(
          param.kind ?? 'SCALAR',
          `${widget.id}'s ${param.name} is a structured parameter; the form cannot edit it and a ` +
            `required one makes the button permanently unsendable`,
        ).toBe('SCALAR');
      }
      for (const param of schema) {
        expect(container.textContent, `${widget.id} rendered no field for parameter ${param.name}`).toContain(
          param.name,
        );
      }
      cleanup();
    }
    expect(checked, 'no command-button was rendered').toBeGreaterThan(0);
  });

  it('gives the entity selector the slot its options target', () => {
    let checked = 0;
    for (const widget of widgetsOf(GALLERY, 'entity-selector')) {
      checked++;
      expect(
        widget.options?.selectionTarget,
        `${widget.id} names no target slot, so the picker drives nothing`,
      ).toBeTruthy();
      // Inert without an ambient provider, so what is asserted here is that it
      // renders at all and says why it is inert rather than throwing.
      const { container } = renderWidget(widget);
      expect(container.textContent).toBeTruthy();
      cleanup();
    }
    expect(checked, 'no entity-selector was rendered').toBeGreaterThan(0);
  });

  it('rounds a value to the precision the options ask for', () => {
    // A table authoring precision must not render the raw float; the stress board's
    // card deliberately authors none and must render it in full.
    let rounded = 0;
    for (const widget of widgetsOf(GALLERY, 'table')) {
      const precision = widget.options?.precision;
      if (typeof precision !== 'number') continue;
      rounded++;
      const { container } = renderWidget(widget);
      expect(container.textContent, `${widget.id} did not round to ${precision} places`).toContain(
        (21.5).toFixed(precision),
      );
      cleanup();
    }
    expect(rounded, 'no table authored a precision, so rounding was never exercised').toBeGreaterThan(0);
  });
});
