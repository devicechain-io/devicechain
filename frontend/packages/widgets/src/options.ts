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

import { type WidgetType } from '@devicechain/dashboards';

// ---- Spec vocabulary --------------------------------------------------------

// `required` marks an option whose ABSENCE leaves the widget with nothing to show.
// The renderer never throws on a missing option — an image with no `url` renders "No
// image URL", a command-button with no `commandName` renders "No command configured" —
// which is deliberate graceful degradation for a hand-edited definition, but is exactly
// the failure a generated dashboard must not ship. So `required` is an AUTHORING
// completeness contract, not a claim that the renderer rejects the option's absence.
//
// Absent here means absent AS THE RENDERER SEES IT, which for a required string includes
// the EMPTY string: every one of the four required options is read into a truthiness test
// (`url ? … : 'No image URL'`, `!commandName`, `!target`), so `''` produces the same
// placeholder as an omitted key. Treating `''` as present would let a generated dashboard
// carrying `"url": ""` validate clean and render nothing.
interface OptionSpecBase {
  // What the option does. Short: this feeds the config panel's help text and the
  // published "available widgets" page.
  doc: string;
  required?: true;
}

export interface StringOptionSpec extends OptionSpecBase {
  kind: 'string';
  // 'json' additionally requires the string to parse as JSON. Used for the command
  // button's baked parameterSchema, where a malformed schema is silently swallowed
  // (parseParameterSchema returns []) and renders as a bare Send button.
  format?: 'json';
}

export interface NumberOptionSpec extends OptionSpecBase {
  kind: 'number';
  min?: number;
  max?: number;
  integer?: true;
}

export interface BooleanOptionSpec extends OptionSpecBase {
  kind: 'boolean';
}

export interface EnumOptionSpec<V extends string = string> extends OptionSpecBase {
  kind: 'enum';
  values: readonly V[];
}

export type OptionSpec = StringOptionSpec | NumberOptionSpec | BooleanOptionSpec | EnumOptionSpec;

export type WidgetOptionSpecs = Record<string, OptionSpec>;

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
  // widget as broken on every board that has not chosen a tile provider yet, which is
  // every board on a fresh instance: there is deliberately NO default tile source,
  // because tiles carry terms the platform cannot accept on an operator's behalf.
  map: {
    title: TITLE,
    tileUrl: {
      kind: 'string',
      doc: 'Raster tile URL template, e.g. https://tiles.example.com/{z}/{x}/{y}.png. Without one, positions are drawn on a plain background.',
    },
    attribution: {
      kind: 'string',
      doc: 'Credit line the tile source requires, shown on the map. Set whatever the provider you chose asks for.',
    },
  },
} as const satisfies Record<WidgetType, WidgetOptionSpecs>;

// ---- Derived compile-time types ---------------------------------------------

// The value type a spec accepts. Enum first: an EnumOptionSpec's values carry the literal
// union, which is the whole point of declaring one.
type SpecValue<S> = S extends { kind: 'enum'; values: readonly (infer V)[] }
  ? V extends string
    ? V
    : never
  : S extends { kind: 'number' }
    ? number
    : S extends { kind: 'boolean' }
      ? boolean
      : S extends { kind: 'string' }
        ? string
        : never;

type RequiredOptionKeys<S> = {
  [K in keyof S]: S[K] extends { required: true } ? K : never;
}[keyof S];

type OptionsFor<T extends WidgetType> = {
  [K in RequiredOptionKeys<(typeof WIDGET_OPTIONS)[T]>]: SpecValue<(typeof WIDGET_OPTIONS)[T][K]>;
} & {
  [K in Exclude<keyof (typeof WIDGET_OPTIONS)[T], RequiredOptionKeys<(typeof WIDGET_OPTIONS)[T]>>]?: SpecValue<
    (typeof WIDGET_OPTIONS)[T][K]
  >;
};

// WidgetOptions<'gauge'> is the typed options bag for a gauge: every declared key at its
// declared type, required ones mandatory. Code that BUILDS a definition should type its
// options with this — a typo is then a compile error rather than a key nothing reads.
//
// DISTRIBUTIVE over T, which is the whole reason it is written as a conditional rather
// than inlining OptionsFor. `keyof` a union of the ten tables is their INTERSECTION — only
// `title` — and RequiredOptionKeys over that union collapses to never, so the undistributed
// form silently degenerates for any generic T: it would reject `{ text: 'body' }` as an
// excess property while accepting `{}` for a widget that requires an option. That is
// exactly the position a generator writing one widget of each type sits in. Distributing
// makes WidgetOptions<WidgetType> the union of the ten per-type bags instead.
export type WidgetOptions<T extends WidgetType> = T extends WidgetType ? OptionsFor<T> : never;

// ---- Runtime validation -----------------------------------------------------

export type OptionIssueCode =
  // a key the renderer for this widget type reads nothing from
  | 'unknown'
  // present, but the wrong JavaScript type — read back as undefined, rendered as default
  | 'type'
  // a string/number outside the option's declared legal values or bounds
  | 'enum'
  | 'range'
  // a `required` option absent, so the widget renders its "not configured" placeholder
  | 'missing'
  // a format:'json' string that does not parse
  | 'json'
  // a rule spanning more than one option (see CROSS_FIELD_RULES)
  | 'invariant';

export interface OptionIssue {
  widgetType: WidgetType;
  // The offending option key. For a cross-field rule, the key the fix belongs on.
  key: string;
  code: OptionIssueCode;
  message: string;
}

type CrossFieldRule = (options: Record<string, unknown>) => Pick<OptionIssue, 'key' | 'code' | 'message'> | undefined;

// Rules that read more than one option, so they cannot live on a single spec. Deliberately
// Partial: most widgets have none, and an exhaustive map would be nine empty entries of
// noise. The table above stays pure data so it can generate the docs page and a
// cross-language type list; these functions are the one exception, kept separate for that
// reason.
const CROSS_FIELD_RULES: Partial<Record<WidgetType, readonly CrossFieldRule[]>> = {
  gauge: [
    (options) => {
      const min = options.min;
      const max = options.max;
      if (typeof min !== 'number' || typeof max !== 'number') return undefined;
      if (!Number.isFinite(min) || !Number.isFinite(max)) return undefined;
      if (max > min) return undefined;
      return {
        key: 'max',
        code: 'invariant',
        message: `max (${max}) must be greater than min (${min})`,
      };
    },
  ],
};

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
  const issues: OptionIssue[] = [];
  const bag = options ?? {};

  for (const [key, spec] of Object.entries(specs)) {
    const value = bag[key];
    // '' counts as absent for a required option: the renderer reads all four into a
    // truthiness test, so an empty string renders the same placeholder as an omitted key.
    if (value === undefined || (spec.required && value === '')) {
      if (spec.required) {
        issues.push({ widgetType, key, code: 'missing', message: `${key} is required but not set` });
      }
      continue;
    }
    issues.push(...checkValue(widgetType, key, spec, value));
  }

  for (const key of Object.keys(bag)) {
    // An OWN-property test, not `in`: every specs table inherits Object.prototype, so `in`
    // reports 'toString', 'constructor' and friends as declared and waves them through. A
    // stored definition arrives via JSON.parse, which makes '__proto__' an ordinary own
    // key — so this is a shape a real definition can carry, not a synthetic one.
    // (hasOwnProperty.call rather than Object.hasOwn: the package targets ES2020.)
    if (Object.prototype.hasOwnProperty.call(specs, key)) continue;
    issues.push({
      widgetType,
      key,
      code: 'unknown',
      message: `${key} is not read by a ${widgetType} widget`,
    });
  }

  for (const rule of CROSS_FIELD_RULES[widgetType] ?? []) {
    const issue = rule(bag);
    if (issue) issues.push({ widgetType, ...issue });
  }

  return issues;
}

function checkValue(widgetType: WidgetType, key: string, spec: OptionSpec, value: unknown): OptionIssue[] {
  const issue = (code: OptionIssueCode, message: string): OptionIssue[] => [{ widgetType, key, code, message }];

  switch (spec.kind) {
    case 'string':
      if (typeof value !== 'string') {
        return issue('type', `${key} must be a string, got ${describe(value)}`);
      }
      if (spec.format === 'json' && !parsesAsJson(value)) {
        return issue('json', `${key} must be valid JSON`);
      }
      return [];

    case 'number':
      // Number.isFinite mirrors optNumber: NaN and ±Infinity survive JSON round-trips
      // through some producers and are read back as undefined by the renderer.
      if (typeof value !== 'number' || !Number.isFinite(value)) {
        return issue('type', `${key} must be a finite number, got ${describe(value)}`);
      }
      if (spec.integer && !Number.isInteger(value)) {
        return issue('range', `${key} must be a whole number, got ${value}`);
      }
      if (spec.min !== undefined && value < spec.min) {
        return issue('range', `${key} must be at least ${spec.min}, got ${value}`);
      }
      if (spec.max !== undefined && value > spec.max) {
        return issue('range', `${key} must be at most ${spec.max}, got ${value}`);
      }
      return [];

    case 'boolean':
      // optBoolean treats anything but literal `true` as false, so a wrong type here is a
      // silently-disabled feature rather than a visible error.
      if (typeof value !== 'boolean') {
        return issue('type', `${key} must be a boolean, got ${describe(value)}`);
      }
      return [];

    case 'enum':
      if (typeof value !== 'string') {
        return issue('type', `${key} must be a string, got ${describe(value)}`);
      }
      if (!spec.values.includes(value)) {
        return issue('enum', `${key} must be one of ${spec.values.join(', ')}, got ${JSON.stringify(value)}`);
      }
      return [];
  }
}

function parsesAsJson(value: string): boolean {
  try {
    JSON.parse(value);
    return true;
  } catch {
    return false;
  }
}

// describe names the offending value's type in a message, quoting a string so an empty
// one is visible and distinguishing null from an object.
function describe(value: unknown): string {
  if (value === null) return 'null';
  if (typeof value === 'string') return `string ${JSON.stringify(value)}`;
  if (Array.isArray(value)) return 'an array';
  return `${typeof value} ${JSON.stringify(value)}`;
}

// numberOptionSpec returns the declared bounds for a numeric option, so an authoring
// UI can constrain its input from the schema rather than from a second, hand-written
// copy of the same numbers.
//
// This exists because the console's numeric inputs were unbounded: a `precision` of
// -1 is a legal number, reaches Number.prototype.toFixed, and throws a RangeError
// DURING RENDER. formatValue clamps so the renderer survives, but the definition is
// still wrong and the author had no way to know. A panel that reads the schema cannot
// offer the value at all.
export function numberOptionSpec(type: WidgetType, key: string): NumberOptionSpec | undefined {
  const specs: WidgetOptionSpecs = WIDGET_OPTIONS[type];
  if (!Object.prototype.hasOwnProperty.call(specs, key)) return undefined;
  const spec = specs[key];
  return spec.kind === 'number' ? spec : undefined;
}

// clampNumberOption folds a raw number into what a numeric option's schema allows, or
// returns undefined when it is not a number at all.
//
// Separate from the input that calls it because the correction is the part worth
// testing: an authoring UI's keystroke handling is awkward to exercise, and the
// question that actually matters — what value gets STORED for a given input — is a
// pure function of the schema and the number.
export function clampNumberOption(spec: NumberOptionSpec | undefined, value: number): number | undefined {
  if (!Number.isFinite(value)) return undefined;
  if (!spec) return value;
  let next = spec.integer ? Math.trunc(value) : value;
  if (spec.min !== undefined) next = Math.max(spec.min, next);
  if (spec.max !== undefined) next = Math.min(spec.max, next);
  return next;
}

// enumOptionValues returns the legal values of an enum option, so an authoring UI can
// build its dropdown from the schema instead of a private copy of the same list.
//
// A dropdown offering a value the schema rejects lets a panel author a definition the
// validator refuses; one MISSING a value silently removes a filter an author could
// previously express. Both are invisible without a shared source.
export function enumOptionValues(type: WidgetType, key: string): readonly string[] | undefined {
  const specs: WidgetOptionSpecs = WIDGET_OPTIONS[type];
  if (!Object.prototype.hasOwnProperty.call(specs, key)) return undefined;
  const spec = specs[key];
  return spec.kind === 'enum' ? spec.values : undefined;
}
