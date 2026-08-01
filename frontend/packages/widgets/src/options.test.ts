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

import { WIDGET_OPTIONS, validateWidgetOptions, type WidgetOptions } from './options';

const SRC = dirname(fileURLToPath(import.meta.url));
// The dashboards package reads a widget's options too (slots.ts), so the gate spans both.
const ROOTS = [SRC, join(SRC, '..', '..', 'dashboards', 'src')];

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
const HELPER_READ = /opt(?:String|Number|Boolean)\(\s*\w+\.options\s*,\s*["']([^"']+)["']\s*,?\s*\)/g;
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
  'command-button': 'widgets/command-button.tsx',
  'entity-selector': 'widgets/entity-selector.tsx',
};

// Files that read options on behalf of MORE THAN ONE widget type — the channel wrapper,
// the shared measurement helper, and the dashboards package's slot-reference walk.
const SHARED_SOURCES = ['connected-widget.tsx', 'widget.ts', '../../dashboards/src/slots.ts'];

// Which types each shared read actually applies to. A shared file cannot say this itself —
// ConnectedWidget reads `state` inside alarmSubscription(), which runs for the alarm
// channel only — so it is declared here and checked against the table. Without it, a key
// deleted from one alarm widget's schema still looks "declared somewhere" and passes.
const SHARED_KEY_TYPES: Record<string, readonly WidgetType[]> = {
  // Every widget renders a frame, and ConnectedWidget's error/unavailable panes read the
  // title to label a widget that never got as far as its own component.
  title: [...WIDGET_TYPES],
  // MeasurementConnectedWidget, for the measurement widgets that bind a datasource.
  window: ['timeseries-chart', 'latest-card', 'gauge', 'table'],
  // alarmSubscription(), which runs for the alarm channel.
  state: ['alarm-table', 'alarm-count'],
  severity: ['alarm-table', 'alarm-count'],
  acknowledged: ['alarm-table', 'alarm-count'],
  // Read by both subscription builders, but alarm-count forces the default page size.
  maxRows: ['alarm-table', 'command-button'],
  // primaryMeasurementName(), called by the two single-value widgets.
  measurement: ['latest-card', 'gauge'],
  // The entity-selector authors it; the alarm-table's originator drill reuses it; slots.ts
  // reads it generically to keep a referenced slot alive.
  selectionTarget: ['alarm-table', 'entity-selector'],
};

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

function optionKeysIn(relativePath: string): string[] {
  const source = readFileSync(join(SRC, relativePath), 'utf8');
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
      const read = sorted([...optionKeysIn(WIDGET_SOURCES[type]), ...shared]);
      expect(sorted(Object.keys(WIDGET_OPTIONS[type])), type).toEqual(read);
    }
  });

  it('gives every option a non-empty doc string', () => {
    for (const [type, specs] of Object.entries(WIDGET_OPTIONS)) {
      for (const [key, spec] of Object.entries(specs)) {
        expect(spec.doc.length, `${type}.${key} has no doc`).toBeGreaterThan(0);
      }
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
  'timeseries-chart': { title: 'Zone temperature', window: 600 },
  'latest-card': { title: 'Temp', measurement: 'temp_c', unit: '°C', precision: 1, flashOnChange: true, window: 300 },
  gauge: { title: 'Humidity', measurement: 'rh', min: 0, max: 100, unit: '%', flashOnChange: false, window: 300 },
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
  'command-button': {
    title: 'Setpoint',
    commandName: 'setSetpoint',
    commandLabel: 'Apply',
    parameterSchema: '[{"name":"target","dataType":"DOUBLE","required":true}]',
    maxRows: 10,
  },
  'entity-selector': { title: 'Thermostat', selectionTarget: 'thermostat' },
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
