// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// WIDGET_OPTIONS — the typed schema for each widget type's `options` bag.
//
// A WidgetInstance's `options` is `Record<string, unknown>` in the stored definition:
// dashboard-management keeps a definition as opaque JSON, and the definition parser does
// not look inside `options` at all. That leaves every option key owned by NOTHING — a
// renamed key breaks no build on either side of the wire, and a wrong-typed value is read
// back as `undefined` by optString/optNumber and rendered as the default, so a
// misconfigured widget is indistinguishable from a correctly-configured one.
//
// This table is that missing owner. It declares, per widget type, every option the
// RENDERER actually reads, with its type and legal values. `as const satisfies
// Record<WidgetType, …>` makes it exhaustive the same way WIDGET_CHANNEL is: a new widget
// type does not compile until it declares an option schema, and an empty schema is then a
// deliberate statement rather than an omission.
//
// Two consumers, one table:
//   • validateWidgetOptions() — the runtime check, for anything that PRODUCES a definition
//     (a generated dashboard must not ship an options blob the renderer ignores).
//   • WidgetOptions<T>        — the compile-time shape, for code that builds options.
//
// The table describes the RENDERER, because that is the contract deciding what a viewer
// actually sees. Where the console's authoring UI offers fewer keys than this — it does:
// `align`, `fontSize`, `fit`, `window` and `measurement` are all honored by the renderer
// and not authorable in the config panel — the gap is in the panel, not here.
//
// The spec VOCABULARY and the engine that checks a bag against a schema live in
// @devicechain/dashboards (option-schema.ts) — they know no widget type, so the SDK's
// registry validates an extension's schema with the very code that validates this one. What
// stays here is the built-in TABLE and thin wrappers that keep the by-type public names.

import {
  clampNumberOption,
  enumValues,
  numberSpec,
  validateOptions,
  type NumberOptionSpec,
  type OptionConstraint,
  type OptionIssueCode,
  type OptionSpec,
  type OptionsOf,
  type WidgetOptionSpecs,
  type WidgetType,
} from '@devicechain/dashboards';

export type {
  BooleanOptionSpec,
  EnumOptionSpec,
  NumberOptionSpec,
  OptionIssueCode,
  OptionSpec,
  StringOptionSpec,
  WidgetOptionSpecs,
} from '@devicechain/dashboards';
export { clampNumberOption };

// ---- Shared option specs ----------------------------------------------------
// Declared once and referenced by each widget that reads them, so the same key always
// carries the same type and bounds. They are still listed PER WIDGET below rather than
// merged in from a base: which widget reads which option is the fact this table exists
// to record, and a base object would hide it.

const TITLE = {
  kind: 'string',
  doc: 'Heading shown in the widget frame. Blank hides the frame heading.',
} as const satisfies OptionSpec;

// gauge and latest-card fall back to the displayed measurement's name when no title is set.
const TITLE_OR_MEASUREMENT = {
  kind: 'string',
  doc: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
} as const satisfies OptionSpec;

// toFixed()'s domain is 0..100 — outside it throws a RangeError, which would take the
// whole widget subtree down rather than degrading.
const PRECISION = {
  kind: 'number',
  integer: true,
  min: 0,
  max: 100,
  doc: 'Decimal places to round displayed values to. Unset shows the raw value.',
} as const satisfies OptionSpec;

const FLASH_ON_CHANGE = {
  kind: 'boolean',
  doc: 'Briefly tint a value green on a rise and red on a fall when it changes.',
} as const satisfies OptionSpec;

// Read by MeasurementConnectedWidget for every measurement-channel widget, and declared on
// each one that binds a datasource. It is NOT declared on label/image, which carry no
// datasource at all, so there is no stream for it to size.
//
// It bounds the RETAINED buffer, not what is drawn: useMeasurementStream keeps `latest`
// per measurement unconditionally and trims only `samples`. Only the chart draws
// `samples`, so on a card, gauge or table this is a memory bound with no visible effect —
// declared because the renderer honors it, documented so nobody expects it to show.
//
// The trim is GLOBAL across the widget's measurements, not per measurement: `samples` is
// one interleaved list, so a chart drawing N series retains roughly window/N of each. An
// earlier version of this doc said "per measurement", and a caller sized a window off it
// and got half the history it intended.
const WINDOW = {
  kind: 'number',
  integer: true,
  min: 1,
  doc: 'Samples retained across ALL of the widget\'s measurements before the oldest are dropped. Bounds memory; only the chart draws them. Default 300.',
} as const satisfies OptionSpec;

const UNIT = {
  kind: 'string',
  doc: 'Unit suffix shown beside the value.',
} as const satisfies OptionSpec;

const ALARM_STATE = {
  kind: 'enum',
  values: ['ACTIVE', 'CLEARED'],
  doc: 'Show only alarms in this state. Unset shows every state.',
} as const satisfies OptionSpec;

const ALARM_SEVERITY = {
  kind: 'enum',
  values: ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'],
  doc: 'Show only alarms at this severity. Unset shows every severity.',
} as const satisfies OptionSpec;

// Stored as the STRING 'true'/'false', not a boolean — alarmSubscription() reads it with
// optString and maps it through parseAcknowledged, so a real boolean here is read as
// undefined and the filter silently disappears. Typed as an enum of those two strings so
// that mistake is an error rather than a no-op.
const ALARM_ACKNOWLEDGED = {
  kind: 'enum',
  values: ['true', 'false'],
  doc: "Show only acknowledged ('true') or unacknowledged ('false') alarms. Unset shows both.",
} as const satisfies OptionSpec;

const SELECTION_TARGET = {
  kind: 'string',
  doc: 'Name of the slot this widget re-points when the viewer picks an entity.',
} as const satisfies OptionSpec;

// ---- The table --------------------------------------------------------------

export const WIDGET_OPTIONS = {
  'timeseries-chart': {
    title: TITLE,
    window: WINDOW,
  },

  'latest-card': {
    title: TITLE_OR_MEASUREMENT,
    measurement: {
      kind: 'string',
      doc: 'Which measurement to display. Defaults to the first the datasource selects.',
    },
    unit: UNIT,
    precision: PRECISION,
    flashOnChange: FLASH_ON_CHANGE,
    window: WINDOW,
  },

  gauge: {
    title: TITLE_OR_MEASUREMENT,
    measurement: {
      kind: 'string',
      doc: 'Which measurement to display. Defaults to the first the datasource selects.',
    },
    min: {
      kind: 'number',
      doc: 'Low end of the gauge scale. Default 0.',
    },
    max: {
      kind: 'number',
      doc: 'High end of the gauge scale. Default 100.',
    },
    unit: UNIT,
    flashOnChange: FLASH_ON_CHANGE,
    window: WINDOW,
  },

  table: {
    title: TITLE,
    precision: PRECISION,
    flashOnChange: FLASH_ON_CHANGE,
    window: WINDOW,
  },

  // label and image carry no datasource: they sit on the measurement channel only
  // because that is ConnectedWidget's default branch, and read no stream data at all.
  label: {
    title: TITLE,
    text: {
      kind: 'string',
      required: true,
      doc: 'The text to show. Rendered as plain text — markup is never interpreted.',
    },
    align: {
      kind: 'enum',
      values: ['left', 'center', 'right'],
      doc: 'Horizontal alignment of the text. Default center.',
    },
    fontSize: {
      kind: 'number',
      min: 1,
      doc: 'Text size in pixels. Default 16.',
    },
  },

  image: {
    title: TITLE,
    url: {
      kind: 'string',
      required: true,
      doc: 'Source URL of the image.',
    },
    alt: {
      kind: 'string',
      doc: 'Alternative text describing the image for assistive technology.',
    },
    fit: {
      kind: 'enum',
      values: ['contain', 'cover', 'fill'],
      doc: 'How the image fills its box. Default contain.',
    },
  },

  'alarm-table': {
    title: TITLE,
    state: ALARM_STATE,
    severity: ALARM_SEVERITY,
    acknowledged: ALARM_ACKNOWLEDGED,
    maxRows: {
      kind: 'number',
      integer: true,
      min: 1,
      doc: 'Alarms loaded into the table. Default 50.',
    },
    precision: PRECISION,
    flashOnChange: FLASH_ON_CHANGE,
    selectionTarget: SELECTION_TARGET,
  },

  // No maxRows: alarmSubscription() forces the default page size for a count, because the
  // number it shows is the SERVER total and the page is read only to sample the worst
  // loaded severity. An authored maxRows here would be read by nothing.
  'alarm-count': {
    title: TITLE,
    state: ALARM_STATE,
    severity: ALARM_SEVERITY,
    acknowledged: ALARM_ACKNOWLEDGED,
  },

  'command-button': {
    title: TITLE,
    commandName: {
      kind: 'string',
      required: true,
      doc: 'Command key issued to the target device. Without it the widget cannot send.',
    },
    commandLabel: {
      kind: 'string',
      doc: 'Text on the send button. Defaults to the command key.',
    },
    parameterSchema: {
      kind: 'string',
      format: 'json',
      doc: "The command's parameter descriptors, baked in as JSON at author time. Drives the typed form.",
    },
    maxRows: {
      kind: 'number',
      integer: true,
      min: 1,
      doc: 'Recent commands shown in the history. Default 20.',
    },
  },

  'entity-selector': {
    title: TITLE,
    selectionTarget: { ...SELECTION_TARGET, required: true },
  },

  // 🔴 tileUrl is NOT `required`, and that is the decision rather than an oversight.
  // `required` means "absent leaves the widget with nothing to show", and a map without
  // a basemap still shows every position it was asked to — on a plain panel, saying why
  // there is no map behind them. Marking it required would report a correctly-rendering
  // widget as broken on every board that names no provider of its own — which is most
  // of them, because the usual and recommended answer is to inherit the tenant's
  // basemap rather than to configure tiles per board.
  map: {
    title: TITLE,
    tileUrl: {
      kind: 'string',
      doc: "Raster tile URL template, e.g. https://tiles.example.com/{z}/{x}/{y}.png. Leave empty to inherit the tenant's basemap; setting one overrides it for this board only.",
    },
    attribution: {
      kind: 'string',
      doc: 'Credit line the tile source requires, shown on the map. Set whatever the provider you chose asks for.',
    },
  },
} as const satisfies Record<WidgetType, WidgetOptionSpecs>;

// ---- Cross-field constraints ------------------------------------------------

// Rules that read more than one option, as DATA (the descriptor's `constraints`). Deliberately
// Partial: most widgets have none, and an exhaustive map would be nine empty entries of
// noise. This used to be a table of closures, which made the schema impossible to serialize.
export const WIDGET_CONSTRAINTS = {
  gauge: [{ kind: 'greater-than', key: 'max', than: 'min' }],
} as const satisfies Partial<Record<WidgetType, readonly OptionConstraint[]>>;

// ---- Derived compile-time types ---------------------------------------------

// WidgetOptions<'gauge'> is the typed options bag for a gauge: every declared key at its
// declared type, required ones mandatory. Code that BUILDS a definition should type its
// options with this — a typo is then a compile error rather than a key nothing reads.
//
// DISTRIBUTIVE over T, which is the whole reason it is written as a conditional. `keyof` a
// union of the built-in tables is their INTERSECTION — only `title` — and the required-keys
// computation over that union collapses to never, so the undistributed form silently
// degenerates for any generic T: it would reject `{ text: 'body' }` as an excess property
// while accepting `{}` for a widget that requires an option. That is exactly the position a
// generator writing one widget of each type sits in. Distributing makes
// WidgetOptions<WidgetType> the union of the per-type bags instead.
export type WidgetOptions<T extends WidgetType> = T extends WidgetType ? OptionsOf<(typeof WIDGET_OPTIONS)[T]> : never;

// ---- Runtime validation -----------------------------------------------------

// The by-type issue shape the console reads. `widgetType` keeps its name so existing
// consumers compile unchanged; the engine's own issue calls the same value `widgetId`.
export interface OptionIssue {
  widgetType: WidgetType;
  // The offending option key. For a cross-field rule, the key the fix belongs on.
  key: string;
  code: OptionIssueCode;
  message: string;
}

function constraintsFor(widgetType: WidgetType): readonly OptionConstraint[] {
  return (WIDGET_CONSTRAINTS as Partial<Record<WidgetType, readonly OptionConstraint[]>>)[widgetType] ?? [];
}

// validateWidgetOptions reports every way a widget's stored options bag disagrees with
// what the renderer reads. An empty result means every key present is one the renderer
// honors, at a value it accepts, and no required option is missing.
//
// It is deliberately a REPORT rather than a throw: the renderer is tolerant by design and
// must stay that way for a hand-edited definition, so the strictness belongs to whoever
// produces definitions (fixtures, generators, the config panel) and to the tests that
// gate them. Callers decide which codes are fatal — a stored dashboard carrying an
// 'unknown' leftover is untidy; a generated one carrying 'missing' is broken.
export function validateWidgetOptions(
  widgetType: WidgetType,
  options: Record<string, unknown> | undefined,
): OptionIssue[] {
  const specs: WidgetOptionSpecs = WIDGET_OPTIONS[widgetType];
  return validateOptions(specs, options, { constraints: constraintsFor(widgetType), label: widgetType }).map(
    ({ key, code, message }) => ({ widgetType, key, code, message }),
  );
}

// numberOptionSpec returns the declared bounds for a numeric option, so an authoring
// UI can constrain its input from the schema rather than from a second, hand-written
// copy of the same numbers. See numberSpec for why it exists.
export function numberOptionSpec(type: WidgetType, key: string): NumberOptionSpec | undefined {
  return numberSpec(WIDGET_OPTIONS[type], key);
}

// enumOptionValues returns the legal values of an enum option, so an authoring UI can
// build its dropdown from the schema instead of a private copy of the same list.
export function enumOptionValues(type: WidgetType, key: string): readonly string[] | undefined {
  return enumValues(WIDGET_OPTIONS[type], key);
}
