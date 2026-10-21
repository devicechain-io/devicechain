// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The drift gate below reads the renderer's own source, so this file needs the Node
// typings. A triple-slash reference is program-wide, not file-scoped — it is written here
// rather than as a tsconfig `types` entry only because this is the one file that needs it,
// and a `types` array would ALSO stop tsc auto-including every other @types package.
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

import { WIDGET_TYPES, type WidgetType } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  WIDGET_OPTIONS,
  validateWidgetOptions,
  numberOptionSpec,
  clampNumberOption,
  enumOptionValues,
  type WidgetOptions,
} from './options';
import { builtinWidgetCatalog } from './builtin-descriptors';
import { WIDGET_BINDS_DATASOURCE, WIDGET_CHANNEL } from './registry';

const SRC = dirname(fileURLToPath(import.meta.url));
// The dashboards package reads a widget's options too (slots.ts), so the gate spans both.
// The widget SDK holds the shared option readers (primaryMeasurementName) the built-ins call,
// so it is scanned too: a read moved out of this package must not leave the gate.
const ROOTS = [SRC, join(SRC, '..', '..', 'dashboards', 'src'), join(SRC, '..', '..', 'widget-sdk', 'src')];

// ---- The drift gate ---------------------------------------------------------
//
// WIDGET_OPTIONS is only worth what it costs to keep true. `satisfies` proves the table
// covers every widget TYPE; nothing in the compiler proves it covers every option KEY,
// because a key is a string literal handed to a helper. So the test reads the renderer's
// own source and requires the declared keys to match the read keys EXACTLY, in both
// directions and per widget type.
//
// Exactness is the point. An "every read key is declared" check passes while a declared
// key is read by nothing (the validator then blesses options the renderer ignores), and it
// passes while the scan itself rots — dropping optBoolean from the pattern below simply
// removes flashOnChange from the evidence, and a one-directional check never notices.
// Requiring set equality makes both of those a failure.

// A read through the option helpers: optString(widget.options, 'key'). Single or double
// quoted, and tolerant of the trailing comma a reformat adds to a wrapped call.
const HELPER_READ = /opt(?:String|Number|Boolean|Thresholds)\(\s*\w+\.options\s*,\s*["']([^"']+)["']\s*,?\s*\)/g;
// A direct read: `w.options?.selectionTarget`. Anchored on the member access so it does not
// match the many unrelated local `options` parameters in this package (chart-options,
// hooks), which are not a widget's bag.
const DIRECT_READ = /\.options\??\.([A-Za-z_$][\w$]*)/g;

// Files whose option reads belong to ONE widget type.
const WIDGET_SOURCES: Record<WidgetType, string> = {
  'timeseries-chart': 'widgets/time-series-chart.tsx',
  'latest-card': 'widgets/latest-card.tsx',
  gauge: 'widgets/gauge.tsx',
  table: 'widgets/table.tsx',
  label: 'widgets/label.tsx',
  image: 'widgets/image.tsx',
  'alarm-table': 'widgets/alarm-table.tsx',
  'alarm-count': 'widgets/alarm-count.tsx',
  'alarm-severity': 'widgets/alarm-severity.tsx',
  'command-button': 'widgets/command-button.tsx',
  'entity-selector': 'widgets/entity-selector.tsx',
  map: 'widgets/map.tsx',
  'linear-gauge': 'widgets/linear-gauge.tsx',
  'level-gauge': 'widgets/level-gauge.tsx',
  battery: 'widgets/battery.tsx',
  'status-light': 'widgets/status-light.tsx',
  'signal-bars': 'widgets/signal-bars.tsx',
  compass: 'widgets/compass.tsx',
  'progress-ring': 'widgets/progress-ring.tsx',
  'command-slider': 'widgets/command-slider.tsx',
  'command-toggle': 'widgets/command-toggle.tsx',
  'stat-card': 'widgets/stat-card.tsx',
  'bar-chart': 'widgets/bar-chart.tsx',
  'pie-chart': 'widgets/pie-chart.tsx',
  'fleet-table': 'widgets/fleet-table.tsx',
  'fleet-count': 'widgets/fleet-count.tsx',
};

// Files that read options on behalf of MORE THAN ONE widget type — the channel wrapper,
// the shared measurement helper, and the dashboards package's slot-reference walk.
//
// Every entry here is evidence that a RENDERER reads a key, which is what makes the
// per-type attribution below mean anything. definition-options.ts briefly appeared in
// this list because it read `title` to label a report — a read that is not renderer
// evidence and needed a paragraph saying so. It reads no option key now, which is the
// better fix: the list means one thing again.
const SHARED_SOURCES = [
  'connected-widget.tsx',
  '../../widget-sdk/src/widget-options.ts',
  '../../dashboards/src/slots.ts',
  // Copy and paste carry the slot a selector re-points (`selectionTarget`) across with the widget.
  '../../dashboards/src/editor-clipboard.ts',
  // lintLayout reads every widget's title to catch one repeating the subject line.
  '../../dashboards/src/layout-lint.ts',
  // The fleet feed reads the per-device bar chart's `measurement` (what its seed must find) and
  // the title of the pane it shows a one-device widget bound to a group.
  'fleet-feed.tsx',
  // The aggregate feed reads the stat card's `sparkline`, to ask for the trend's buckets.
  'aggregate-feed.tsx',
  // The chart's event markers: binding-policy decides whether they are wanted, and the feed reads
  // which channels (alarms, commands) to draw.
  'binding-policy.ts',
  'event-markers-feed.tsx',
];

// Which types each shared read actually applies to. A shared file cannot say this itself —
// ConnectedWidget reads `state` inside alarmSubscription(), which runs for the alarm
// channel only — so it is declared here and checked against the table. Without it, a key
// deleted from one alarm widget's schema still looks "declared somewhere" and passes.
const SHARED_KEY_TYPES: Record<string, readonly WidgetType[]> = {
  // Every widget renders a frame, and ConnectedWidget's error/unavailable panes read the
  // title to label a widget that never got as far as its own component.
  title: [...WIDGET_TYPES],
  // primaryMeasurementName(), called by the two single-value widgets.
  // (and the level widgets, through the shared useLevelReading hook).
  // The fleet feed reads it for the bar chart's one bar per device.
  measurement: ['latest-card', 'gauge', 'linear-gauge', 'level-gauge', 'battery', 'status-light', 'signal-bars', 'compass', 'progress-ring', 'stat-card', 'bar-chart', 'fleet-count'],
  // The entity-selector authors it; the alarm-table's originator drill reuses it; slots.ts
  // reads it generically to keep a referenced slot alive, and editor-clipboard.ts to carry it
  // across a paste.
  selectionTarget: ['alarm-table', 'entity-selector'],
  // aggregate-feed.tsx asks for the trend's buckets when the stat card is set to draw one.
  sparkline: ['stat-card'],
  // binding-policy.ts and event-markers-feed.tsx read which markers the timeseries chart draws.
  markAlarms: ['timeseries-chart'],
  markCommands: ['timeseries-chart'],
};

// The option keys a widget's BINDING POLICY references (window, page size, alarm filters).
// The channel adapters read these by the name the descriptor declares, with no literal in
// any source file for the scan above to find, so a policy reference is itself the evidence of
// a read — the same standing a literal `optNumber(widget.options, 'x')` had when
// connected-widget.tsx held them. binding-policy.test.ts proves the adapters honor them.
function policyKeys(type: WidgetType): string[] {
  const keys: string[] = [];
  const walk = (value: unknown): void => {
    if (typeof value !== 'object' || value === null) return;
    for (const [k, v] of Object.entries(value)) {
      if (k === 'option' && typeof v === 'string') keys.push(v);
      else walk(v);
    }
  };
  walk(builtinWidgetCatalog.get(type)?.binding);
  return keys;
}

// The option a widget's FRAME reads on its behalf when its descriptor opts in to freshness
// (`freshness: true`): the host hands the frame the threshold, so no widget source holds the
// read. The pin in the describe below proves the read still exists where it is claimed.
function frameKeys(type: WidgetType): string[] {
  return builtinWidgetCatalog.get(type)?.freshness === true ? ['staleAfterSeconds'] : [];
}

function sourceFiles(): string[] {
  const out: string[] = [];
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(full);
    }
  };
  for (const root of ROOTS) walk(root);
  return out;
}

// stripComments blanks out comments before a scan.
//
// 🔴 A regex over raw source cannot tell code from prose, so the same text sitting in
// a comment satisfies a scan looking for a call — the retired read coming back to life
// as documentation. Verified: replacing a widget's optBoolean read with `false` and
// leaving the old call in a comment kept every gate green, while the schema went on
// declaring, and validateWidgetOptions went on blessing, an option the renderer no
// longer read. Blanked rather than removed so multi-line patterns still span the same
// distance.
function stripComments(source: string): string {
  const blank = (m: string) => m.replace(/[^\n]/g, ' ');
  return source.replace(/\/\*[\s\S]*?\*\//g, blank).replace(/\/\/.*$/gm, blank);
}

function optionKeysIn(relativePath: string): string[] {
  const source = stripComments(readFileSync(join(SRC, relativePath), 'utf8'));
  return [
    ...[...source.matchAll(HELPER_READ)].map((m) => m[1]),
    ...[...source.matchAll(DIRECT_READ)].map((m) => m[1]),
  ];
}

const sorted = (keys: Iterable<string>): string[] => [...new Set(keys)].sort();

describe('the option-read scan', () => {
  // Negative control. If the pattern stops matching, every assertion below passes for the
  // wrong reason — so pin that each widget source yields reads, and that the pattern still
  // catches all THREE helpers (dropping just optBoolean from the alternation would
  // otherwise slip past a "non-empty" check).
  it('finds option reads in every widget source', () => {
    for (const [type, path] of Object.entries(WIDGET_SOURCES)) {
      expect(optionKeysIn(path), `no option reads found in ${path} (for ${type})`).not.toHaveLength(0);
    }
  });

  it('catches reads through each of the three option helpers', () => {
    // One key per helper, each read in exactly this shape today: optString → 'text',
    // optNumber → 'fontSize', optBoolean → 'flashOnChange'.
    expect(optionKeysIn(WIDGET_SOURCES.label)).toEqual(expect.arrayContaining(['text', 'fontSize']));
    expect(optionKeysIn(WIDGET_SOURCES.table)).toContain('flashOnChange');
  });

  it('catches a direct member read outside the widgets package', () => {
    // dashboards/src/slots.ts reads `w.options?.selectionTarget` — a different package, a
    // different call shape, and the read the helper pattern alone would miss entirely.
    expect(optionKeysIn('../../dashboards/src/slots.ts')).toContain('selectionTarget');
  });

  // The closed-world check. A read added to a file nobody listed is invisible to every
  // assertion above, so the file list is required to be exactly the set of files that read
  // options at all — a new widget, helper or subcomponent that reads one fails here until
  // it is classified as per-type or shared.
  it('lists exactly the files that read a widget’s options', () => {
    const found = sourceFiles()
      .filter((file) => optionKeysIn(relative(SRC, file)).length > 0)
      .map((file) => relative(SRC, file).split('\\').join('/'));
    const listed = [...Object.values(WIDGET_SOURCES), ...SHARED_SOURCES];
    expect(sorted(found)).toEqual(sorted(listed));
  });
});

describe('frame-read option references', () => {
  it('pins that the freshness threshold is still read from the options bag', () => {
    const sdk = readFileSync(join(SRC, '..', '..', 'widget-sdk', 'src', 'freshness.ts'), 'utf8');
    expect(stripComments(sdk)).toMatch(/optNumber\(\s*options\s*,\s*'staleAfterSeconds'\s*\)/);
    expect(frameKeys('latest-card')).toEqual(['staleAfterSeconds']);
    expect(frameKeys('label')).toEqual([]);
  });
});

describe('binding-policy option references', () => {
  // Negative control for the policy half of the evidence: if the walk stopped finding
  // references, the declared-equals-read check would fail only for the keys it used to supply
  // — pin that it still finds them, per channel.
  it('finds the references each channel declares', () => {
    expect(policyKeys('timeseries-chart')).toEqual(['windowSeconds']);
    expect(policyKeys('alarm-table').sort()).toEqual(['acknowledged', 'groupBy', 'maxRows', 'severity', 'state']);
    expect(policyKeys('alarm-count').sort()).toEqual(['acknowledged', 'severity', 'state']); // page size is a constant
    expect(policyKeys('alarm-severity').sort()).toEqual(['acknowledged', 'deviceType', 'state']); // it breaks severity down, so none filters it
    expect(policyKeys('command-button')).toEqual(['maxRows']);
  });
});

describe('WIDGET_OPTIONS', () => {
  it('declares a schema for every widget type', () => {
    expect(Object.keys(WIDGET_OPTIONS).sort()).toEqual([...WIDGET_TYPES].sort());
  });

  it('attributes every shared read to the types it applies to', () => {
    // SHARED_KEY_TYPES is hand-written, so it is pinned to the scan: a new key read by a
    // shared path has to be attributed before it can be declared anywhere.
    expect(sorted(SHARED_SOURCES.flatMap(optionKeysIn))).toEqual(sorted(Object.keys(SHARED_KEY_TYPES)));
  });

  it('declares exactly the options each widget reads — no more, no fewer', () => {
    for (const type of WIDGET_TYPES) {
      const shared = Object.entries(SHARED_KEY_TYPES)
        .filter(([, types]) => types.includes(type))
        .map(([key]) => key);
      const read = sorted([...optionKeysIn(WIDGET_SOURCES[type]), ...shared, ...policyKeys(type), ...frameKeys(type)]);
      expect(sorted(Object.keys(WIDGET_OPTIONS[type])), type).toEqual(read);
    }
  });
});

// ---- Compile-time contract ---------------------------------------------------
// These assert on the DERIVED type rather than on runtime behaviour: each @ts-expect-error
// fails the build if the error it claims does not occur, so tsc is the assertion. CI runs
// typecheck before the tests, so this half is gated.

describe('WidgetOptions<T>', () => {
  it('constrains a bag built for a concrete widget type', () => {
    // A generic producer — one widget of each type — is the position this type exists for.
    const build = <T extends WidgetType>(type: T, options: WidgetOptions<T>) => ({ type, options });

    expect(build('gauge', { min: 0, max: 10 }).options.max).toBe(10);
    expect(build('entity-selector', { selectionTarget: 'thermostat' }).options.title).toBeUndefined();

    // @ts-expect-error — selectionTarget is required on an entity-selector.
    build('entity-selector', {});
    // @ts-expect-error — 'precison' is a typo, not an option a table reads.
    build('table', { precison: 2 });
    // @ts-expect-error — max is a number, not a string.
    build('gauge', { max: '10' });
    // @ts-expect-error — 'scale-down' is not one of the fit values.
    build('image', { url: 'u', fit: 'scale-down' });
    // @ts-expect-error — acknowledged is the STRING 'true', never a boolean.
    build('alarm-table', { acknowledged: true });
  });

  it('stays per-type when T is the whole union', () => {
    // WidgetOptions is distributive, so the union form is the union of the ten bags rather
    // than their (near-empty) key intersection. Undistributed, both of these would fail:
    // `text` would be an excess property and `selectionTarget` would not exist.
    const any: WidgetOptions<WidgetType>[] = [{ title: 'x', text: 'body' }, { selectionTarget: 's' }];
    expect(any).toHaveLength(2);
  });
});

// ---- validateWidgetOptions --------------------------------------------------

// A known-good options bag per widget type. The counterweight to every rejection test
// below: refusing bad input is only safe while good input passes untouched.
const VALID: { [T in WidgetType]: WidgetOptions<T> } = {
  'timeseries-chart': { title: 'Zone temperature', windowSeconds: 600 },
  'latest-card': { title: 'Temp', measurement: 'temp_c', unit: '°C', precision: 1, flashOnChange: true, window: 300 },
  gauge: { title: 'Humidity', measurement: 'rh', min: 0, max: 100, unit: '%', precision: 1, flashOnChange: false, window: 300 },
  table: { title: 'All measurements', precision: 2, flashOnChange: true, window: 120 },
  label: { title: '', text: 'Floor 3', align: 'left', fontSize: 24 },
  image: { title: 'Floor plan', url: 'https://example.invalid/plan.png', alt: 'Floor plan', fit: 'cover' },
  'alarm-table': {
    title: 'Active alarms',
    state: 'ACTIVE',
    severity: 'CRITICAL',
    acknowledged: 'false',
    maxRows: 25,
    precision: 1,
    flashOnChange: true,
    selectionTarget: 'thermostat',
  },
  'alarm-count': { title: 'Open', state: 'ACTIVE', severity: 'MAJOR', acknowledged: 'true' },
  'alarm-severity': { title: 'By severity', state: 'ACTIVE', acknowledged: 'false', deviceType: 'thermostat' },
  'command-button': {
    title: 'Setpoint',
    commandName: 'setSetpoint',
    commandLabel: 'Apply',
    parameterSchema: '[{"name":"target","dataType":"DOUBLE","required":true}]',
    maxRows: 10,
  },
  'entity-selector': { title: 'Thermostat', selectionTarget: 'thermostat' },
  map: {
    title: 'Fleet',
    tileUrl: 'https://tiles.example.invalid/{z}/{x}/{y}.png',
    attribution: '© Example Tiles',
  },
  'linear-gauge': {
    title: 'Tank temperature',
    measurement: 'temp_c',
    min: 0,
    max: 120,
    style: 'thermometer',
    orientation: 'vertical',
    unit: 'C',
    precision: 1,
    thresholds: [{ value: 90, level: 'major' }],
    window: 300,
  },
  'level-gauge': { title: 'Tank', measurement: 'level', min: 0, max: 5000, readout: 'percent', unit: 'L', precision: 0, window: 300 },
  battery: { title: 'Battery', measurement: 'soc', min: 3, max: 4.2, unit: 'V', precision: 2, window: 300 },
  'status-light': {
    title: 'Pump',
    measurement: 'running',
    onAt: 1,
    thresholds: [{ value: 0, level: 'critical', label: 'Stopped' }],
    window: 300,
  },
  'signal-bars': {
    title: 'Signal',
    measurement: 'rssi',
    unit: 'dBm',
    thresholds: [
      { value: -110, level: 'critical' },
      { value: -100, level: 'warning' },
      { value: -90, level: 'normal' },
    ],
    window: 300,
  },
  compass: {
    title: 'Wind',
    measurement: 'wind_dir',
    precision: 0,
    thresholds: [{ value: 90, level: 'warning' }],
    window: 300,
  },
  'progress-ring': { title: 'Fill', measurement: 'fill', min: 0, max: 200, readout: 'value', unit: 'L', precision: 0, window: 300 },
  'command-slider': {
    title: 'Setpoint',
    commandName: 'setSetpoint',
    commandLabel: 'Set Setpoint',
    parameterSchema: '[{"name":"target","dataType":"DOUBLE","required":true}]',
    parameter: 'target',
    min: 15,
    max: 35,
    step: 0.5,
  },
  'command-toggle': {
    title: 'Power',
    commandName: 'setPower',
    commandLabel: 'Set Power',
    parameterSchema: '[{"name":"enabled","dataType":"BOOLEAN","required":true}]',
    parameter: 'enabled',
  },
  'stat-card': {
    title: 'Peak temperature',
    measurement: 'temp_c',
    stat: 'max',
    unit: 'C',
    precision: 1,
    thresholds: [{ value: 90, level: 'major' }],
    sparkline: true,
    windowSeconds: 3600,
  },
  'bar-chart': { title: 'Readings', groupBy: 'device', measurement: 'temp_c', unit: 'C', precision: 1, sort: 'descending' },
  'pie-chart': { title: 'Share', unit: 'L', precision: 0, sort: 'ascending' },
  'fleet-table': {
    title: 'Machines',
    columns: [{ measurement: 'temp_c', label: 'Temperature', unit: 'C', precision: 1 }],
    thresholds: [{ measurement: 'temp_c', value: 90, level: 'major' }],
    staleAfterSeconds: 300,
    ageLagSeconds: 30,
    sortBy: 'temp_c',
    sortDescending: true,
    pageSize: 25,
    selectionTarget: 'machine',
    windowSeconds: 3600,
  },
  'fleet-count': {
    title: 'Low fuel',
    show: 'attention',
    measurement: 'fuel',
    thresholds: [{ value: 0, level: 'critical', label: 'Low' }, { value: 15, level: 'normal' }],
    staleAfterSeconds: 300,
    windowSeconds: 3600,
  },
};

describe('validateWidgetOptions', () => {
  it('accepts a fully-populated valid bag for every widget type', () => {
    for (const type of WIDGET_TYPES) {
      expect(validateWidgetOptions(type, VALID[type] as Record<string, unknown>), type).toEqual([]);
    }
  });

  it('accepts an omitted optional option', () => {
    expect(validateWidgetOptions('gauge', { title: 'g' })).toEqual([]);
  });

  it('reports a required option that is absent', () => {
    // An image with no url renders "No image URL" — a placeholder, not an error, which is
    // exactly why a generated dashboard has to be told.
    expect(validateWidgetOptions('image', { title: 'Plan' })).toEqual([
      { widgetType: 'image', key: 'url', code: 'missing', message: expect.stringContaining('url') },
    ]);
  });

  it('reports a required option present as an empty string', () => {
    // Every required option is read into a truthiness test, so '' renders the same
    // placeholder as an omitted key — and a bag carrying "url": "" would otherwise be
    // certified clean while the widget shows nothing.
    expect(validateWidgetOptions('image', { url: '' }).map((i) => i.code)).toEqual(['missing']);
    expect(validateWidgetOptions('label', { text: '' }).map((i) => i.code)).toEqual(['missing']);
    expect(validateWidgetOptions('command-button', { commandName: '' }).map((i) => i.code)).toEqual(['missing']);
    expect(validateWidgetOptions('entity-selector', { selectionTarget: '' }).map((i) => i.code)).toEqual(['missing']);
  });

  it('still accepts an empty string for an option that is not required', () => {
    // A blank title is a real choice: it hides the frame heading.
    expect(validateWidgetOptions('label', { title: '', text: 'x' })).toEqual([]);
  });

  it('treats undefined as absent rather than as a wrong-typed value', () => {
    expect(validateWidgetOptions('gauge', { title: undefined, min: 0, max: 10 })).toEqual([]);
  });

  it('reports an entirely missing options bag against required options', () => {
    expect(validateWidgetOptions('entity-selector', undefined)).toEqual([
      { widgetType: 'entity-selector', key: 'selectionTarget', code: 'missing', message: expect.any(String) },
    ]);
    expect(validateWidgetOptions('timeseries-chart', undefined)).toEqual([]);
  });

  it('reports a key the widget does not read', () => {
    // maxRows is honored on alarm-table and ignored on alarm-count, which is precisely the
    // kind of near-miss that looks configured and does nothing.
    const issues = validateWidgetOptions('alarm-count', { title: 'Open', maxRows: 10 });
    expect(issues).toEqual([{ widgetType: 'alarm-count', key: 'maxRows', code: 'unknown', message: expect.any(String) }]);
  });

  it('reports a misspelled key rather than silently dropping it', () => {
    const issues = validateWidgetOptions('gauge', { title: 'g', minimum: 0 });
    expect(issues.map((i) => [i.key, i.code])).toEqual([['minimum', 'unknown']]);
  });

  it('reports a key that only LOOKS declared because Object.prototype has it', () => {
    // The schema tables are plain object literals, so `'toString' in specs` is true. A
    // membership test that trusted that would wave these through unchecked — and
    // '__proto__' is not hypothetical: JSON.parse makes it an ordinary own key, and a
    // stored dashboard definition is exactly a JSON.parse result.
    for (const key of ['toString', 'constructor', 'valueOf', 'hasOwnProperty']) {
      expect(validateWidgetOptions('gauge', { [key]: 'garbage' }).map((i) => [i.key, i.code]), key).toEqual([
        [key, 'unknown'],
      ]);
    }
    const parsed = JSON.parse('{"__proto__": {"junk": true}, "title": "g"}') as Record<string, unknown>;
    expect(validateWidgetOptions('gauge', parsed).map((i) => i.code)).toEqual(['unknown']);
  });

  it('reports a wrong-typed string', () => {
    const issues = validateWidgetOptions('label', { text: 42 });
    expect(issues).toEqual([{ widgetType: 'label', key: 'text', code: 'type', message: expect.any(String) }]);
  });

  it('reports a number given as a string', () => {
    // optNumber reads this back as undefined, so the gauge silently rescales to 0..100.
    const issues = validateWidgetOptions('gauge', { min: '0', max: 50 });
    expect(issues.map((i) => [i.key, i.code])).toEqual([['min', 'type']]);
  });

  it('reports a non-finite number', () => {
    for (const bad of [Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(validateWidgetOptions('gauge', { min: 0, max: bad }).map((i) => i.code)).toContain('type');
    }
  });

  it('reports null distinctly from absent', () => {
    // JSON carries null; the renderer reads it back as undefined and shows the default.
    expect(validateWidgetOptions('gauge', { min: null }).map((i) => i.code)).toEqual(['type']);
  });

  it('reports a fractional value for an integer option', () => {
    const issues = validateWidgetOptions('alarm-table', { maxRows: 12.5 });
    expect(issues.map((i) => [i.key, i.code])).toEqual([['maxRows', 'range']]);
  });

  it('reports a precision outside toFixed()’s domain', () => {
    // Below 0 or above 100, Number.prototype.toFixed throws a RangeError. formatValue
    // clamps so the renderer degrades instead of unmounting, but the value is still wrong
    // and a producer should hear about it.
    expect(validateWidgetOptions('table', { precision: -1 }).map((i) => i.code)).toEqual(['range']);
    expect(validateWidgetOptions('table', { precision: 101 }).map((i) => i.code)).toEqual(['range']);
    expect(validateWidgetOptions('table', { precision: 0 })).toEqual([]);
    expect(validateWidgetOptions('table', { precision: 100 })).toEqual([]);
  });

  it('reports a maxRows below the clamp the renderer would silently apply', () => {
    expect(validateWidgetOptions('command-button', { commandName: 'x', maxRows: 0 }).map((i) => i.code)).toEqual([
      'range',
    ]);
  });

  it('reports a value outside an enum', () => {
    const issues = validateWidgetOptions('image', { url: 'u', fit: 'scale-down' });
    expect(issues).toEqual([{ widgetType: 'image', key: 'fit', code: 'enum', message: expect.stringContaining('contain') }]);
  });

  it('reports a lower-cased alarm state', () => {
    expect(validateWidgetOptions('alarm-count', { state: 'active' }).map((i) => i.code)).toEqual(['enum']);
  });

  it('reports acknowledged given as a real boolean', () => {
    // The stored form is the STRING 'true'/'false'; alarmSubscription reads it with
    // optString, so a boolean here makes the filter vanish and the widget shows every
    // alarm — a strictly wider result set that looks like a working widget.
    const issues = validateWidgetOptions('alarm-table', { acknowledged: true });
    expect(issues).toEqual([{ widgetType: 'alarm-table', key: 'acknowledged', code: 'type', message: expect.any(String) }]);
    expect(validateWidgetOptions('alarm-table', { acknowledged: 'true' })).toEqual([]);
  });

  it('reports a wrong-typed boolean flag', () => {
    // optBoolean treats anything but literal true as false, so 'true' silently disables it.
    const issues = validateWidgetOptions('table', { flashOnChange: 'true' });
    expect(issues.map((i) => [i.key, i.code])).toEqual([['flashOnChange', 'type']]);
  });

  it('reports a parameterSchema that is not JSON', () => {
    // parseParameterSchema swallows a parse failure and returns [], rendering a bare Send
    // button — a command that looks parameterless rather than broken.
    const issues = validateWidgetOptions('command-button', { commandName: 'x', parameterSchema: '[{name:1}]' });
    expect(issues).toEqual([
      { widgetType: 'command-button', key: 'parameterSchema', code: 'json', message: expect.any(String) },
    ]);
  });

  it('accepts a well-formed parameterSchema', () => {
    expect(validateWidgetOptions('command-button', { commandName: 'x', parameterSchema: '[]' })).toEqual([]);
  });

  it('reports a gauge whose scale is inverted or empty', () => {
    expect(validateWidgetOptions('gauge', { min: 100, max: 0 }).map((i) => [i.key, i.code])).toEqual([
      ['max', 'invariant'],
    ]);
    expect(validateWidgetOptions('gauge', { min: 50, max: 50 }).map((i) => i.code)).toEqual(['invariant']);
    expect(validateWidgetOptions('gauge', { min: -20, max: 0 })).toEqual([]);
  });

  it('does not raise the cross-field rule when one side is absent or already wrong', () => {
    // The invariant must not add noise on top of a type error it cannot evaluate.
    expect(validateWidgetOptions('gauge', { min: 100 })).toEqual([]);
    expect(validateWidgetOptions('gauge', { min: '100', max: 0 }).map((i) => i.code)).toEqual(['type']);
  });

  it('reports every problem in a bag rather than stopping at the first', () => {
    const issues = validateWidgetOptions('alarm-table', {
      state: 'nope',
      maxRows: -3,
      flashOnChange: 1,
      bogus: true,
    });
    expect(issues.map((i) => [i.key, i.code]).sort()).toEqual(
      [
        ['bogus', 'unknown'],
        ['flashOnChange', 'type'],
        ['maxRows', 'range'],
        ['state', 'enum'],
      ].sort(),
    );
  });
});

// ---- The authoring surface reads the schema ----------------------------------

describe('numberOptionSpec', () => {
  it('returns the declared bounds for a numeric option', () => {
    expect(numberOptionSpec('table', 'precision')).toMatchObject({ kind: 'number', min: 0, max: 100, integer: true });
    expect(numberOptionSpec('alarm-table', 'maxRows')).toMatchObject({ kind: 'number', min: 1, integer: true });
  });

  it('returns nothing for an option that is not numeric, or not read at all', () => {
    expect(numberOptionSpec('table', 'title')).toBeUndefined();
    expect(numberOptionSpec('alarm-count', 'maxRows')).toBeUndefined();
    // An Object.prototype name must not resolve to a spec, for the same reason the
    // unknown-key check uses an own-property test.
    expect(numberOptionSpec('gauge', 'toString')).toBeUndefined();
  });

  // The reason this accessor exists: an authoring UI that reads it cannot offer a
  // value the renderer would have to clamp. `precision` below 0 reaches
  // Number.prototype.toFixed and throws a RangeError during render.
  it('bounds precision to the domain toFixed can survive', () => {
    const spec = numberOptionSpec('latest-card', 'precision');
    expect(spec?.min).toBe(0);
    expect(spec?.max).toBe(100);
    expect(() => (1.5).toFixed(spec?.min as number)).not.toThrow();
    expect(() => (1.5).toFixed(spec?.max as number)).not.toThrow();
    expect(() => (1.5).toFixed((spec?.min as number) - 1)).toThrow();
  });
});

describe('WIDGET_BINDS_DATASOURCE', () => {
  it('classifies every widget type', () => {
    expect(Object.keys(WIDGET_BINDS_DATASOURCE).sort()).toEqual([...WIDGET_TYPES].sort());
  });

  // The structural half, and only that half.
  //
  // An earlier version required `window` to be declared exactly where a widget both
  // binds a datasource AND sits on the measurement channel. That is a curation
  // choice dressed as a fact: the option is read channel-wide, so an alarm widget
  // that legitimately grew a buffer bound would have had to rename it or misdeclare
  // its binding to get a green suite — and the failure said "declares window but
  // binds no datasource" about a widget that binds one, which is simply wrong.
  //
  // What IS structural is the direction that cannot be a preference: a widget with no
  // datasource has no stream for a window to size.
  it('declares no stream option on a widget that binds no stream', () => {
    for (const type of WIDGET_TYPES) {
      if (WIDGET_BINDS_DATASOURCE[type]) continue;
      expect(
        'window' in WIDGET_OPTIONS[type],
        `${type} binds no datasource, so there is no stream for its window to size`,
      ).toBe(false);
    }
  });

  it('claims no datasource for a widget on the selection channel', () => {
    // A selection widget drives a slot through its options and reads no data at all.
    for (const type of WIDGET_TYPES) {
      if (WIDGET_CHANNEL[type] !== 'selection') continue;
      expect(
        WIDGET_BINDS_DATASOURCE[type],
        `${type} is a selection widget but claims to bind a datasource`,
      ).toBe(false);
    }
  });
})

describe('clampNumberOption', () => {
  const precision = numberOptionSpec('table', 'precision');
  const maxRows = numberOptionSpec('alarm-table', 'maxRows');
  const unbounded = numberOptionSpec('gauge', 'min');

  it('folds a value into the declared bounds', () => {
    expect(clampNumberOption(precision, -1)).toBe(0);
    expect(clampNumberOption(precision, 101)).toBe(100);
    expect(clampNumberOption(precision, 2)).toBe(2);
    expect(clampNumberOption(maxRows, 0)).toBe(1);
  });

  it('truncates toward zero on an integer option', () => {
    expect(clampNumberOption(precision, 2.7)).toBe(2);
    expect(clampNumberOption(maxRows, 12.9)).toBe(12);
  });

  it('leaves an unbounded option alone, including a negative one', () => {
    // A gauge's scale legitimately starts below zero, so the absence of bounds in the
    // schema has to mean the absence of a clamp — not a default of zero.
    expect(clampNumberOption(unbounded, -20)).toBe(-20);
    expect(clampNumberOption(unbounded, 1e6)).toBe(1e6);
  });

  it('reports a non-number as absent rather than coercing it', () => {
    // The input hands over Number(raw); a field holding only "-" parses to NaN, and
    // storing that would put a value in the definition that reads back as undefined.
    expect(clampNumberOption(precision, Number.NaN)).toBeUndefined();
    expect(clampNumberOption(precision, Number.POSITIVE_INFINITY)).toBeUndefined();
    expect(clampNumberOption(undefined, Number.NaN)).toBeUndefined();
  });

  it('passes a value through when the option has no schema at all', () => {
    // An option this package does not declare is not one it gets to bound.
    expect(clampNumberOption(undefined, 42)).toBe(42);
  });

  // The clamp and the validator have to agree, or the panel authors values the gate
  // then rejects. Every clamped result must validate clean.
  it('produces values the validator accepts', () => {
    for (const raw of [-100, -1, 0, 0.5, 2.7, 99, 101, 1000]) {
      const value = clampNumberOption(precision, raw);
      expect(validateWidgetOptions('table', { title: 't', precision: value }), `precision from ${raw}`).toEqual(
        [],
      );
    }
  });
});

describe('enumOptionValues', () => {
  it('returns the legal values an authoring dropdown must offer', () => {
    expect(enumOptionValues('alarm-table', 'state')).toEqual(['ACTIVE', 'CLEARED']);
    expect(enumOptionValues('alarm-table', 'acknowledged')).toEqual(['true', 'false']);
    expect(enumOptionValues('image', 'fit')).toEqual(['contain', 'cover', 'fill']);
  });

  it('returns nothing for an option that is not an enum, or not read at all', () => {
    expect(enumOptionValues('table', 'precision')).toBeUndefined();
    expect(enumOptionValues('table', 'nope')).toBeUndefined();
    expect(enumOptionValues('gauge', 'toString')).toBeUndefined();
  });

  // The point of the accessor: what a panel offers and what the validator accepts are
  // the same list, so a dropdown cannot author a value the gate then refuses.
  it('offers exactly what the validator accepts', () => {
    let checked = 0;
    for (const [type, specs] of Object.entries(WIDGET_OPTIONS)) {
      for (const [key, spec] of Object.entries(specs)) {
        if (spec.kind !== 'enum') continue;
        const values = enumOptionValues(type as WidgetType, key);
        expect(values, `${type}.${key}`).toBeDefined();
        for (const value of values as readonly string[]) {
          checked++;
          expect(
            validateWidgetOptions(type as WidgetType, { [key]: value }).filter((i) => i.key === key),
            `${type}.${key} offers ${value} but the validator rejects it`,
          ).toEqual([]);
        }
      }
    }
    expect(checked, 'no enum option was checked at all').toBeGreaterThan(0);
  });
});
