// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reads the committed fixtures off disk; see the note in options.test.ts.
/// <reference types="node" />

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import type { AlarmRow, CommandRow, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { parseDashboardDefinition } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

// jsdom has no canvas, so the ECharts wrapper is stubbed — as in widgets.test.tsx.
// The stub still receives the real option object the widget built from its options
// bag, so a chart's configuration is exercised even though nothing is painted.
vi.mock('./echart', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  EChart: ({ option }: { option: any }) => <div data-testid="echart" data-option={JSON.stringify(option)} />,
}));

import type { AlarmStreamState, CommandStreamState, MeasurementStreamState } from './hooks';
import {
  ALARM_WIDGET_REGISTRY,
  CONTROL_WIDGET_REGISTRY,
  SELECTION_WIDGET_REGISTRY,
  WIDGET_CHANNEL,
  WIDGET_REGISTRY,
} from './registry';

afterEach(cleanup);

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

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'widgetlab');

const GALLERY = 'wl-gallery';
const STRESS = 'wl-stress';

function board(token: string) {
  return parseDashboardDefinition(JSON.parse(readFileSync(join(FIXTURES, `${token}.json`), 'utf8')));
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
const EMPTY_STATES = ['No image URL', 'No command configured'];

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
  message: 'over temperature',
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
const commandData: CommandStreamState = {
  deviceToken: 'wl-sensor-01',
  commands: [commandRow],
  total: 1,
  loading: false,
  error: null,
};

// renderWidget runs one widget through the registry its channel binds it to.
function renderWidget(widget: WidgetInstance) {
  switch (WIDGET_CHANNEL[widget.type]) {
    case 'alarm': {
      const Component = ALARM_WIDGET_REGISTRY[widget.type as keyof typeof ALARM_WIDGET_REGISTRY];
      return render(<Component widget={widget} data={alarmData} />);
    }
    case 'control': {
      const Component = CONTROL_WIDGET_REGISTRY[widget.type as keyof typeof CONTROL_WIDGET_REGISTRY];
      return render(<Component widget={widget} data={commandData} />);
    }
    case 'selection': {
      const Component = SELECTION_WIDGET_REGISTRY[widget.type as keyof typeof SELECTION_WIDGET_REGISTRY];
      return render(<Component widget={widget} data={null} />);
    }
    default: {
      const Component = WIDGET_REGISTRY[widget.type as keyof typeof WIDGET_REGISTRY];
      return render(<Component widget={widget} data={measurementData()} />);
    }
  }
}

describe('rendering the widgetlab boards', () => {
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
  function galleryWidgets(type: string): WidgetInstance[] {
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
  function widgetsOf(token: string, type: string): WidgetInstance[] {
    return board(token).widgets.filter((w) => w.type === type);
  }

  it('renders every title a widget authored', () => {
    let checked = 0;
    for (const token of [GALLERY, STRESS]) {
      for (const widget of board(token).widgets) {
        const title = widget.options?.title;
        if (typeof title !== 'string' || title === '') continue;
        if (WIDGET_CHANNEL[widget.type] === 'selection') continue;
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
