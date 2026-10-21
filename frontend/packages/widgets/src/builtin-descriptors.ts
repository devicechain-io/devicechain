// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The portable descriptor of every built-in widget — pure data, React-free, importing no
// component. The headless validators read the catalog built from these without ever
// loading a widget; builtin-registry.ts pairs each descriptor with its component.
//
// 🔑 THE SOURCE OF TRUTH. A built-in's channel, option schema, defaults and binding policy are
// declared here, once, as data. Nothing else in this package keeps an id-keyed table of them:
// the by-id views the console still reads (WIDGET_CHANNEL, WIDGET_OPTIONS, ...) are derived,
// read-only projections of these descriptors (registry.ts, options.ts).
//
// Every built-in carries version 1.0.0: a per-widget release number, NOT the platform
// version. It is bumped only on a rendering change worth surfacing to an author, so a
// platform release does not make every board look "authored with an older widget".

import { SEVERITY_LEVELS } from '@devicechain/widget-sdk';
import {
  createWidgetCatalog,
  defineWidgetDescriptor,
  type BuiltinWidgetId,
  type OptionSpec,
  type WidgetDescriptor,
} from '@devicechain/dashboards';

// ---- Shared option specs ----------------------------------------------------
// Declared once and referenced by each widget that reads them, so the same key always
// carries the same type and bounds. They are still listed PER WIDGET below rather than
// merged in from a base: which widget reads which option is the fact this table exists
// to record, and a base object would hide it.

const TITLE = {
  kind: 'string',
  labelId: 'widgetLabelTitle',
  doc: 'Heading shown in the widget frame. Blank hides the frame heading.',
} as const satisfies OptionSpec;

// gauge and latest-card fall back to the displayed measurement's name when no title is set.
const TITLE_OR_MEASUREMENT = {
  kind: 'string',
  labelId: 'widgetLabelTitle',
  doc: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
} as const satisfies OptionSpec;

// toFixed()'s domain is 0..100 — outside it throws a RangeError, which would take the
// whole widget subtree down rather than degrading.
const PRECISION = {
  kind: 'number',
  integer: true,
  min: 0,
  max: 100,
  labelId: 'widgetLabelPrecision',
  helpId: 'widgetHintPrecision',
  doc: 'Decimal places to round displayed values to. Unset rounds to at most 3 decimals (4 significant digits under 1).',
} as const satisfies OptionSpec;

// Where a measurement crosses from one severity to the next. A threshold STARTS a band that
// runs to the next threshold's value, so "overheat above 105" is one entry and "low fuel below
// 15" is two (critical from 0, normal from 15). `level` is a severity NAME that the theme turns
// into a colour; there is deliberately no colour field, so a board cannot carry a hex value
// that ignores dark mode or the host's theme. Optional with no default: absent means no bands,
// exactly as before the option existed, so it needs no optionsVersion bump.
const THRESHOLDS = {
  kind: 'list',
  maxItems: 12,
  labelId: 'widgetLabelThresholds',
  helpId: 'widgetHintThresholds',
  doc: 'Severity bands over the value. Each entry starts a band at its value that runs to the next entry; below the first, and in a normal band, the widget keeps its own colour.',
  item: {
    value: {
      kind: 'number',
      required: true,
      labelId: 'widgetLabelThresholdValue',
      doc: 'The value at which this band starts.',
    },
    level: {
      kind: 'enum',
      required: true,
      values: SEVERITY_LEVELS,
      labelId: 'widgetLabelThresholdLevel',
      valueLabelIds: {
        normal: 'widgetSeverityNormal',
        warning: 'widgetSeverityWarning',
        minor: 'widgetSeverityMinor',
        major: 'widgetSeverityMajor',
        critical: 'widgetSeverityCritical',
      },
      placeholderId: 'widgetPlaceholderThresholdLevel',
      doc: 'The severity of this band. The theme decides the colour; normal has none.',
    },
    label: {
      kind: 'string',
      labelId: 'widgetLabelThresholdLabel',
      doc: 'Text shown on the chart line for this threshold. Defaults to its value.',
    },
  },
} as const satisfies OptionSpec;

const FLASH_ON_CHANGE = {
  kind: 'boolean',
  labelId: 'widgetFlashOnChange',
  helpId: 'widgetFlashOnChangeHint',
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
  labelId: 'widgetLabelWindow',
  helpId: 'widgetHintWindow',
  doc: 'Samples retained across ALL of the widget\'s measurements before the oldest are dropped. Bounds memory; only the chart draws them. Default 300.',
} as const satisfies OptionSpec;

// Read by the frame, not by the widget: with it set, a widget whose newest sample is older than
// this many seconds is greyed out under a "stale" badge. Unset is the DEFAULT and means age
// only -- there is no built-in threshold, because any number would paint a sparse fleet stale.
const STALE_AFTER_SECONDS = {
  kind: 'number',
  integer: true,
  min: 1,
  labelId: 'widgetLabelStaleAfter',
  helpId: 'widgetHintStaleAfter',
  doc: 'Seconds after which the newest sample is called stale: the value is greyed out and badged. Unset shows the sample age only and never marks the widget stale.',
} as const satisfies OptionSpec;

const UNIT = {
  kind: 'string',
  labelId: 'widgetLabelUnit',
  doc: 'Unit suffix shown beside the value.',
} as const satisfies OptionSpec;

// The two ends of a level widget's scale. REQUIRED, with no default on the descriptor: a tank
// or a thermometer drawn against a range nobody chose reads a wrong value as a plausible one,
// so a widget without both ends says so instead of drawing (see level-reading.ts). The
// greater-than constraint on each descriptor keeps the pair ordered.
const SCALE_MIN = {
  kind: 'number',
  required: true,
  labelId: 'widgetLabelMin',
  helpId: 'widgetHintScaleMin',
  doc: 'Value at the empty end of the scale. Required: the widget does not guess a range.',
} as const satisfies OptionSpec;

const SCALE_MAX = {
  kind: 'number',
  required: true,
  labelId: 'widgetLabelMax',
  helpId: 'widgetHintScaleMax',
  doc: 'Value at the full end of the scale. Required, and above min.',
} as const satisfies OptionSpec;

// The command a control sends. The three keys are the command-vocabulary triple the console's
// command picker writes at once (the command button declares the same three), so picking a
// command in the editor bakes its key, label and parameter schema here too.
const CONTROL_COMMAND_NAME = {
  kind: 'string',
  required: true,
  labelId: 'widgetCommandHeading',
  helpId: 'widgetCommandFieldHint',
  control: 'command-vocabulary',
  doc: 'Command key issued to the target device. Without it the control cannot send.',
} as const satisfies OptionSpec;

const CONTROL_COMMAND_LABEL = {
  kind: 'string',
  labelId: 'widgetCommandHeading',
  helpId: 'widgetCommandFieldHint',
  control: 'command-vocabulary',
  doc: 'Name of the control for assistive technology. Defaults to the command key.',
} as const satisfies OptionSpec;

const CONTROL_PARAMETER_SCHEMA = {
  kind: 'string',
  format: 'json',
  labelId: 'widgetCommandHeading',
  helpId: 'widgetCommandFieldHint',
  control: 'command-vocabulary',
  doc: "The command's parameter descriptors, baked in as JSON at author time. Gives the control its range and its refusals.",
} as const satisfies OptionSpec;

const CONTROL_PARAMETER = {
  kind: 'string',
  labelId: 'widgetLabelParameter',
  helpId: 'widgetHintControlParameter',
  doc: 'Which parameter of the command this control sets. May be left blank when the command has exactly one parameter of the right type.',
} as const satisfies OptionSpec;

const MEASUREMENT = {
  kind: 'string',
  labelId: 'widgetLabelMeasurement',
  helpId: 'widgetHintMeasurement',
  doc: 'Which measurement to display. Defaults to the first the datasource selects.',
} as const satisfies OptionSpec;

const ALARM_STATE = {
  kind: 'enum',
  values: ['ACTIVE', 'CLEARED'],
  labelId: 'widgetLabelState',
  helpId: 'widgetStateHint',
  valueLabelIds: { ACTIVE: 'widgetAlarmStateActive', CLEARED: 'widgetAlarmStateCleared' },
  placeholderId: 'widgetPlaceholderAny',
  doc: 'Show only alarms in this state. Unset shows every state.',
} as const satisfies OptionSpec;

const ALARM_SEVERITY = {
  kind: 'enum',
  values: ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'],
  labelId: 'widgetLabelSeverity',
  helpId: 'widgetSeverityHint',
  valueLabelIds: { CRITICAL: 'widgetAlarmSeverityCritical', MAJOR: 'widgetAlarmSeverityMajor', MINOR: 'widgetAlarmSeverityMinor', WARNING: 'widgetAlarmSeverityWarning', INDETERMINATE: 'widgetAlarmSeverityIndeterminate' },
  placeholderId: 'widgetPlaceholderAny',
  doc: 'Show only alarms at this severity. Unset shows every severity.',
} as const satisfies OptionSpec;

// Stored as the STRING 'true'/'false', not a boolean — alarmSubscription() reads it with
// optString and maps it through parseAcknowledged, so a real boolean here is read as
// undefined and the filter silently disappears. Typed as an enum of those two strings so
// that mistake is an error rather than a no-op.
const ALARM_ACKNOWLEDGED = {
  kind: 'enum',
  values: ['true', 'false'],
  labelId: 'widgetLabelAcknowledged',
  helpId: 'widgetAcknowledgedHint',
  valueLabelIds: { true: 'widgetAlarmAckAcknowledged', false: 'widgetAlarmAckUnacknowledged' },
  placeholderId: 'widgetPlaceholderAny',
  doc: "Show only acknowledged ('true') or unacknowledged ('false') alarms. Unset shows both.",
} as const satisfies OptionSpec;

const SELECTION_TARGET = {
  kind: 'string',
  labelId: 'widgetLabelTargetSlot',
  helpId: 'widgetTargetSlotHint',
  control: 'slot-target',
  doc: 'Name of the slot this widget re-points when the viewer picks an entity.',
} as const satisfies OptionSpec;


// Each widget's `layout` is its size contract, in SQUARE cells. A cell is never smaller than the
// grid's minCellSize (56 px by default; a narrower board reflows instead), so a span is a
// readable size by construction, and the minimums below are sized at that smallest cell, with
// 8 px gutters: n cells cover 64n - 8 px, so 2 is 120 px, 3 is 184 px and 4 is 248 px. On a wider
// screen the cells stretch and every widget only grows.
//
// The minimums are the smallest box in which the widget's parts all fit with room to spare:
// height is usually the binding constraint, since the frame header costs ~34-50 px before the
// widget's own content. The defaults are what a freshly added widget takes. A maximum is
// declared only where the content stops filling the box (a single value, a dial, a button, a
// one-line selector); a chart, table, map or image fills a whole wall, so it has none, and a
// label's column limit is set past any authored column count.

const VERSION = '1.0.0';
const ALL_SELECTORS = ['device', 'anchor', 'slot'] as const;
// A telemetry widget that shows readings keyed by measurement name shows ONE device: bound to a group,
// two devices reporting the same name would overwrite each other in it. Only the fleet widgets (the
// fleet table, a bar chart with one bar per device) take a group, and the runtime refuses a group
// that reaches any other one some other way (a host binding, a hand-edited definition).
const ONE_DEVICE = ['device', 'slot'] as const;
const NO_DATASOURCE = { mode: 'none' } as const;

// The alarm filters are read by both alarm widgets from the same option keys.
const ALARM_FILTERS = {
  state: { option: 'state' },
  severity: { option: 'severity' },
  acknowledged: { option: 'acknowledged' },
} as const;

// The timeseries chart's window, as a SPAN. Unset, the chart follows the board's time range (which
// a viewer can change from the header); set, it is PINNED to this span whatever the viewer picks,
// which is what a "last 5 minutes" strip on a wall board wants. The bounds are the board range's:
// a minute to a week.
const WINDOW_SECONDS = {
  kind: 'number',
  integer: true,
  min: 60,
  max: 604800,
  labelId: 'widgetLabelWindowSeconds',
  helpId: 'widgetHintWindowSeconds',
  doc: 'Seconds of history the widget covers, back from now. Leave unset to follow the board’s time range; set it to pin this widget to a fixed span that the viewer’s range picker does not change. 60 to 604800.',
} as const satisfies OptionSpec;

// The categorical charts' shared options. Both draw the latest reading of each measurement the
// datasource names: the order is the datasource's own unless a sort is set.
const CATEGORY_SORT = {
  kind: 'enum',
  values: ['none', 'ascending', 'descending'],
  labelId: 'widgetLabelSort',
  valueLabelIds: { none: 'widgetSortNone', ascending: 'widgetSortAscending', descending: 'widgetSortDescending' },
  placeholderId: 'widgetPlaceholderDefault',
  doc: 'Order of the categories. Default: as the data source lists them.',
} as const satisfies OptionSpec;

// A fleet table's per-column and per-column-threshold entries NAME their measurement, because a list
// item holds scalars only: one flat list per concern keeps each editable as a row, and a column
// without an entry keeps the datasource's name, the board's formatting and no bands.
// The measurement column a fleet table's list entry (a format, a band) applies to.
const FLEET_MEASUREMENT = {
  kind: 'string',
  required: true,
  labelId: 'widgetLabelMeasurement',
  doc: 'The measurement column this entry applies to.',
} as const satisfies OptionSpec;

const FLEET_COLUMNS = {
  kind: 'list',
  maxItems: 24,
  labelId: 'widgetLabelColumns',
  helpId: 'widgetHintColumns',
  doc: 'Heading, unit and precision per measurement column; a column with none shows its name.',
  item: {
    measurement: FLEET_MEASUREMENT,
    label: { kind: 'string', labelId: 'widgetLabelColumnHeading', doc: 'Column heading. Defaults to the measurement name.' },
    unit: { ...UNIT },
    precision: { ...PRECISION },
  },
} as const satisfies OptionSpec;

const FLEET_THRESHOLDS = {
  ...THRESHOLDS,
  item: {
    measurement: FLEET_MEASUREMENT,
    ...THRESHOLDS.item,
  },
} as const satisfies OptionSpec;

export const builtinWidgetDescriptors = {
  'timeseries-chart': defineWidgetDescriptor({
    id: 'timeseries-chart',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 2,
    display: { title: 'Timeseries chart', category: 'Telemetry', titleId: 'widgetTypeTimeseriesChart', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { windowSeconds: { option: 'windowSeconds' } },
    options: {
      title: TITLE,
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      windowSeconds: WINDOW_SECONDS,
    },
    defaults: { title: 'Timeseries chart' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 6, rows: 4 }, minSpan: { cols: 4, rows: 3 } },
  }),

  'latest-card': defineWidgetDescriptor({
    id: 'latest-card',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Latest card', category: 'Telemetry', titleId: 'widgetTypeLatestCard', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: {
        kind: 'string',
        labelId: 'widgetLabelMeasurement',
        helpId: 'widgetHintMeasurement',
        doc: 'Which measurement to display. Defaults to the first the datasource selects.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      flashOnChange: FLASH_ON_CHANGE,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Latest card' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 3, rows: 2 }, minSpan: { cols: 3, rows: 2 }, maxSpan: { cols: 6, rows: 4 } },
  }),

  gauge: defineWidgetDescriptor({
    id: 'gauge',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Gauge', category: 'Telemetry', titleId: 'widgetTypeGauge', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: {
        kind: 'string',
        labelId: 'widgetLabelMeasurement',
        helpId: 'widgetHintMeasurement',
        doc: 'Which measurement to display. Defaults to the first the datasource selects.',
      },
      min: {
        kind: 'number',
        labelId: 'widgetLabelMin',
        doc: 'Low end of the gauge scale. Default 0.',
      },
      max: {
        kind: 'number',
        labelId: 'widgetLabelMax',
        doc: 'High end of the gauge scale. Default 100.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      flashOnChange: FLASH_ON_CHANGE,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Gauge' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 3, rows: 3 }, minSpan: { cols: 3, rows: 3 }, maxSpan: { cols: 6, rows: 6 } },
  }),

  table: defineWidgetDescriptor({
    id: 'table',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Table', category: 'Telemetry', titleId: 'widgetTypeTable', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE,
      precision: PRECISION,
      flashOnChange: FLASH_ON_CHANGE,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Table' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 6, rows: 3 }, minSpan: { cols: 4, rows: 2 } },
  }),

  // label and image are static: the `none` channel, no datasource, no subscription and no
  // history seed.
  label: defineWidgetDescriptor({
    id: 'label',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Label', category: 'Static', titleId: 'widgetTypeLabel', categoryId: 'widgetCategoryStatic' },
    channel: 'none',
    datasource: NO_DATASOURCE,
    binding: {},
    options: {
      title: TITLE,
      text: {
        kind: 'string',
        required: true,
        labelId: 'widgetLabelText',
        doc: 'The text to show. Rendered as plain text — markup is never interpreted.',
      },
      align: {
        kind: 'enum',
        values: ['left', 'center', 'right'],
        labelId: 'widgetLabelAlign',
        valueLabelIds: { left: 'widgetAlignLeft', center: 'widgetAlignCenter', right: 'widgetAlignRight' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'Horizontal alignment of the text. Default center.',
      },
      fontSize: {
        kind: 'number',
        min: 1,
        labelId: 'widgetLabelFontSize',
        helpId: 'widgetHintFontSize',
        doc: 'Text size in pixels at the comfortable density; it scales with the board density. Default 16.',
      },
    },
    defaults: { text: 'New label' },
    interactions: {},
    layout: { defaultSpan: { cols: 3, rows: 1 }, minSpan: { cols: 1, rows: 1 }, maxSpan: { cols: 24, rows: 3 } },
  }),

  image: defineWidgetDescriptor({
    id: 'image',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Image', category: 'Static', titleId: 'widgetTypeImage', categoryId: 'widgetCategoryStatic' },
    channel: 'none',
    datasource: NO_DATASOURCE,
    binding: {},
    options: {
      title: TITLE,
      url: {
        kind: 'string',
        required: true,
        labelId: 'widgetLabelImageUrl',
        doc: 'Source URL of the image.',
      },
      alt: {
        kind: 'string',
        labelId: 'widgetLabelAltText',
        doc: 'Alternative text describing the image for assistive technology.',
      },
      fit: {
        kind: 'enum',
        values: ['contain', 'cover', 'fill'],
        labelId: 'widgetLabelFit',
        valueLabelIds: { contain: 'widgetFitContain', cover: 'widgetFitCover', fill: 'widgetFitFill' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'How the image fills its box. Default contain.',
      },
    },
    defaults: { title: 'Image' },
    interactions: {},
    layout: { defaultSpan: { cols: 3, rows: 2 }, minSpan: { cols: 1, rows: 1 } },
  }),

  'alarm-table': defineWidgetDescriptor({
    id: 'alarm-table',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Alarm table', category: 'Alarms', titleId: 'widgetTypeAlarmTable', categoryId: 'widgetCategoryAlarms' },
    channel: 'alarm',
    datasource: { mode: 'optional', selectors: ALL_SELECTORS },
    binding: { pageSize: { option: 'maxRows', default: 50, min: 1 }, ...ALARM_FILTERS },
    options: {
      title: TITLE,
      state: ALARM_STATE,
      severity: ALARM_SEVERITY,
      acknowledged: ALARM_ACKNOWLEDGED,
      maxRows: {
        kind: 'number',
        integer: true,
        min: 1,
        labelId: 'widgetLabelMaxRows',
        helpId: 'widgetMaxRowsAlarmHint',
        doc: 'Alarms loaded into the table. Default 50.',
      },
      precision: PRECISION,
      flashOnChange: FLASH_ON_CHANGE,
      selectionTarget: SELECTION_TARGET,
    },
    defaults: { title: 'Alarm table', state: 'ACTIVE' },
    interactions: { alarmActions: true, selection: true },
    layout: { defaultSpan: { cols: 6, rows: 4 }, minSpan: { cols: 4, rows: 2 } },
  }),

  // The count shows the SERVER total and reads a page only to sample the worst loaded
  // severity, so its page size is a constant rather than an authorable option.
  'alarm-count': defineWidgetDescriptor({
    id: 'alarm-count',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Alarm count', category: 'Alarms', titleId: 'widgetTypeAlarmCount', categoryId: 'widgetCategoryAlarms' },
    channel: 'alarm',
    datasource: { mode: 'optional', selectors: ALL_SELECTORS },
    binding: { pageSize: { const: 50 }, ...ALARM_FILTERS },
    // No maxRows: alarmSubscription() forces the default page size for a count, because the
    // number it shows is the SERVER total and the page is read only to sample the worst
    // loaded severity. An authored maxRows here would be read by nothing.
    options: {
      title: TITLE,
      state: ALARM_STATE,
      severity: ALARM_SEVERITY,
      acknowledged: ALARM_ACKNOWLEDGED,
    },
    defaults: { title: 'Alarm count', state: 'ACTIVE' },
    interactions: {},
    layout: { defaultSpan: { cols: 3, rows: 2 }, minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 6, rows: 4 } },
  }),

  'command-button': defineWidgetDescriptor({
    id: 'command-button',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Command button', category: 'Control', titleId: 'widgetTypeCommandButton', categoryId: 'widgetCategoryControl' },
    channel: 'control',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { pageSize: { option: 'maxRows', default: 20, min: 1 } },
    options: {
      title: TITLE,
      commandName: {
        kind: 'string',
        required: true,
        labelId: 'widgetCommandHeading',
        helpId: 'widgetCommandFieldHint',
        control: 'command-vocabulary',
        doc: 'Command key issued to the target device. Without it the widget cannot send.',
      },
      commandLabel: {
        kind: 'string',
        labelId: 'widgetCommandHeading',
        helpId: 'widgetCommandFieldHint',
        control: 'command-vocabulary',
        doc: 'Text on the send button. Defaults to the command key.',
      },
      parameterSchema: {
        kind: 'string',
        format: 'json',
        labelId: 'widgetCommandHeading',
        helpId: 'widgetCommandFieldHint',
        control: 'command-vocabulary',
        doc: "The command's parameter descriptors, baked in as JSON at author time. Drives the typed form.",
      },
      maxRows: {
        kind: 'number',
        integer: true,
        min: 1,
        labelId: 'widgetLabelMaxRows',
        helpId: 'widgetMaxRowsCommandHint',
        doc: 'Recent commands shown in the history. Default 20.',
      },
    },
    defaults: { title: 'Command button' },
    interactions: { commandSend: true },
    layout: { defaultSpan: { cols: 3, rows: 3 }, minSpan: { cols: 3, rows: 2 }, maxSpan: { cols: 6, rows: 6 } },
  }),

  'entity-selector': defineWidgetDescriptor({
    id: 'entity-selector',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Entity selector', category: 'Navigation', titleId: 'widgetTypeEntitySelector', categoryId: 'widgetCategoryNavigation' },
    channel: 'selection',
    datasource: NO_DATASOURCE,
    binding: {},
    options: {
      title: TITLE,
      selectionTarget: { ...SELECTION_TARGET, required: true },
    },
    defaults: { title: 'Entity selector' },
    interactions: { selection: true, candidates: true },
    // A select control is one line high, so its rows are pinned; it widens to fit long names.
    layout: { defaultSpan: { cols: 3, rows: 1 }, minSpan: { cols: 3, rows: 1 }, maxSpan: { cols: 12, rows: 1 }, resize: 'horizontal' },
  }),

  map: defineWidgetDescriptor({
    id: 'map',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Map', category: 'Location', titleId: 'widgetTypeMap', categoryId: 'widgetCategoryLocation' },
    channel: 'location',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: {},
    // 🔴 tileUrl is NOT `required`, and that is the decision rather than an oversight.
    // `required` means "absent leaves the widget with nothing to show", and a map without
    // a basemap still shows every position it was asked to — on a plain panel, saying why
    // there is no map behind them. Marking it required would report a correctly-rendering
    // widget as broken on every board that names no provider of its own — which is most
    // of them, because the usual and recommended answer is to inherit the tenant's
    // basemap rather than to configure tiles per board.
    options: {
      title: TITLE,
      tileUrl: {
        kind: 'string',
        labelId: 'widgetLabelTileUrl',
        helpId: 'widgetTileUrlHint',
        control: 'basemap',
        doc: "Raster tile URL template, e.g. https://tiles.example.com/{z}/{x}/{y}.png. Leave empty to inherit the tenant's basemap; setting one overrides it for this board only.",
      },
      attribution: {
        kind: 'string',
        labelId: 'widgetLabelAttribution',
        helpId: 'widgetAttributionHint',
        control: 'basemap',
        doc: 'Credit line the tile source requires, shown on the map. Set whatever the provider you chose asks for.',
      },
      // Optional and additive: unset, a marker is only a mark. Set, clicking a marker re-points
      // that slot at the device, exactly as a fleet-table row does.
      selectionTarget: SELECTION_TARGET,
    },
    defaults: { title: 'Map' },
    interactions: { selection: true },
    layout: { defaultSpan: { cols: 6, rows: 4 }, minSpan: { cols: 4, rows: 3 } },
  }),

  // ---- Levels and indicators ------------------------------------------------
  // DOM/SVG single-value widgets on the measurement channel. A threshold band colours the fill
  // or lamp; `normal` keeps the widget's neutral accent.

  'linear-gauge': defineWidgetDescriptor({
    id: 'linear-gauge',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Linear gauge', category: 'Telemetry', titleId: 'widgetTypeLinearGauge', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      min: SCALE_MIN,
      max: SCALE_MAX,
      style: {
        kind: 'enum',
        values: ['bar', 'thermometer'],
        labelId: 'widgetLabelLinearStyle',
        valueLabelIds: { bar: 'widgetLinearStyleBar', thermometer: 'widgetLinearStyleThermometer' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'A plain bar (default) or a thermometer: a vertical tube with a bulb. A thermometer is always vertical.',
      },
      orientation: {
        kind: 'enum',
        values: ['horizontal', 'vertical'],
        labelId: 'widgetLabelOrientation',
        valueLabelIds: { horizontal: 'widgetOrientationHorizontal', vertical: 'widgetOrientationVertical' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'Direction of a bar: horizontal (default) or vertical. Ignored by a thermometer.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Linear gauge' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 4, rows: 3 }, minSpan: { cols: 3, rows: 3 }, maxSpan: { cols: 8, rows: 8 } },
  }),

  'level-gauge': defineWidgetDescriptor({
    id: 'level-gauge',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Level gauge', category: 'Telemetry', titleId: 'widgetTypeLevelGauge', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      min: SCALE_MIN,
      max: SCALE_MAX,
      readout: {
        kind: 'enum',
        values: ['value', 'percent'],
        labelId: 'widgetLabelReadout',
        valueLabelIds: { value: 'widgetReadoutValue', percent: 'widgetReadoutPercent' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'What the readout shows: the value with its unit (default) or the percent of the scale.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Level gauge' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 3, rows: 4 }, minSpan: { cols: 2, rows: 3 }, maxSpan: { cols: 6, rows: 8 } },
  }),

  battery: defineWidgetDescriptor({
    id: 'battery',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Battery', category: 'Telemetry', titleId: 'widgetTypeBattery', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      min: {
        kind: 'number',
        labelId: 'widgetLabelMin',
        helpId: 'widgetHintBatteryRange',
        doc: 'Value of an empty battery. Default 0 (the scale is percent unless you set min and max).',
      },
      max: {
        kind: 'number',
        labelId: 'widgetLabelMax',
        helpId: 'widgetHintBatteryRange',
        doc: 'Value of a full battery. Default 100 (the scale is percent unless you set min and max).',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Battery' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 3, rows: 2 }, minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 6, rows: 4 } },
  }),

  'status-light': defineWidgetDescriptor({
    id: 'status-light',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Status light', category: 'Telemetry', titleId: 'widgetTypeStatusLight', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      onAt: {
        kind: 'number',
        labelId: 'widgetLabelOnAt',
        helpId: 'widgetHintOnAt',
        doc: 'For an on/off signal: the lamp is lit at or above this value and dark below it. Unset keeps the lamp lit while a value is present, coloured by its threshold band.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Status light' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 2, rows: 2 }, minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 4, rows: 4 } },
  }),

  'signal-bars': defineWidgetDescriptor({
    id: 'signal-bars',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Signal bars', category: 'Telemetry', titleId: 'widgetTypeSignalBars', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      unit: UNIT,
      precision: PRECISION,
      // One bar per threshold: the entries ARE the steps, so they are required.
      thresholds: {
        ...THRESHOLDS,
        maxItems: 8,
        required: true,
        helpId: 'widgetHintSignalThresholds',
        doc: 'The steps of the signal, lowest first: one bar per entry, lit once the value reaches it and coloured by its level. At least two, at most eight.',
      },
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Signal bars' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 2, rows: 2 }, minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 4, rows: 4 } },
  }),

  // ---- Radial family ----------------------------------------------------------
  // A heading on a full turn, and a ring filled between a configured min and max. The compass
  // draws on the registered gauge series; the ring is SVG. Both put their number in DOM text.

  compass: defineWidgetDescriptor({
    id: 'compass',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Compass', category: 'Telemetry', titleId: 'widgetTypeCompass', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      precision: {
        ...PRECISION,
        doc: 'Decimal places of the heading. Default 0 (whole degrees). The heading is rounded first, then folded onto 0 to 360, so it never reads 360.',
      },
      thresholds: {
        ...THRESHOLDS,
        doc: 'Severity bands over the heading in degrees (0 to 360, after folding). Each entry starts a band at its value that runs to the next entry; below the first, and in a normal band, the needle keeps its own colour.',
      },
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Compass' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 4, rows: 4 }, minSpan: { cols: 3, rows: 3 }, maxSpan: { cols: 8, rows: 8 } },
  }),

  'progress-ring': defineWidgetDescriptor({
    id: 'progress-ring',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Progress ring', category: 'Telemetry', titleId: 'widgetTypeProgressRing', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      min: SCALE_MIN,
      max: SCALE_MAX,
      readout: {
        kind: 'enum',
        values: ['percent', 'value'],
        labelId: 'widgetLabelReadout',
        valueLabelIds: { percent: 'widgetReadoutPercent', value: 'widgetReadoutValue' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'What the middle of the ring shows: the percent of the scale (default) or the value with its unit.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Progress ring' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 3, rows: 3 }, minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 6, rows: 6 } },
  }),

  'command-slider': defineWidgetDescriptor({
    id: 'command-slider',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Command slider', category: 'Control', titleId: 'widgetTypeCommandSlider', categoryId: 'widgetCategoryControl' },
    channel: 'control',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    // The control reads its last-sent value off this history, so it asks for a long one.
    binding: { pageSize: { const: 50 } },
    options: {
      title: TITLE,
      commandName: CONTROL_COMMAND_NAME,
      commandLabel: CONTROL_COMMAND_LABEL,
      parameterSchema: CONTROL_PARAMETER_SCHEMA,
      parameter: CONTROL_PARAMETER,
      min: {
        kind: 'number',
        labelId: 'widgetLabelMin',
        helpId: 'widgetHintSliderRange',
        doc: "Lowest value, used only when the command's parameter declares no minimum.",
      },
      max: {
        kind: 'number',
        labelId: 'widgetLabelMax',
        helpId: 'widgetHintSliderRange',
        doc: "Highest value, used only when the command's parameter declares no maximum.",
      },
      step: {
        kind: 'number',
        min: 0,
        labelId: 'widgetLabelStep',
        helpId: 'widgetHintSliderStep',
        doc: 'Distance between values. Defaults to 1 for a whole-number parameter, else a hundredth of the range.',
      },
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Command slider' },
    interactions: { commandSend: true },
    layout: { defaultSpan: { cols: 4, rows: 2 }, minSpan: { cols: 3, rows: 2 }, maxSpan: { cols: 8, rows: 3 } },
  }),

  'command-toggle': defineWidgetDescriptor({
    id: 'command-toggle',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Command toggle', category: 'Control', titleId: 'widgetTypeCommandToggle', categoryId: 'widgetCategoryControl' },
    channel: 'control',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { pageSize: { const: 50 } },
    options: {
      title: TITLE,
      commandName: CONTROL_COMMAND_NAME,
      commandLabel: CONTROL_COMMAND_LABEL,
      parameterSchema: CONTROL_PARAMETER_SCHEMA,
      parameter: CONTROL_PARAMETER,
    },
    defaults: { title: 'Command toggle' },
    interactions: { commandSend: true },
    layout: { defaultSpan: { cols: 3, rows: 2 }, minSpan: { cols: 2, rows: 2 }, maxSpan: { cols: 6, rows: 3 } },
  }),
  'stat-card': defineWidgetDescriptor({
    id: 'stat-card',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Stat card', category: 'Telemetry', titleId: 'widgetTypeStatCard', categoryId: 'widgetCategoryTelemetry' },
    channel: 'aggregate',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: { windowSeconds: { option: 'windowSeconds' } },
    options: {
      title: TITLE_OR_MEASUREMENT,
      measurement: MEASUREMENT,
      stat: {
        kind: 'enum',
        values: ['avg', 'min', 'max', 'sum', 'count', 'last'],
        labelId: 'widgetLabelStat',
        helpId: 'widgetHintStat',
        valueLabelIds: {
          avg: 'widgetStatAvg',
          min: 'widgetStatMin',
          max: 'widgetStatMax',
          sum: 'widgetStatSum',
          count: 'widgetStatCount',
          last: 'widgetStatLast',
        },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'Which figure to show. Default average.',
      },
      unit: UNIT,
      precision: PRECISION,
      thresholds: THRESHOLDS,
      windowSeconds: WINDOW_SECONDS,
    },
    defaults: { title: 'Stat card' },
    interactions: {},
    layout: { defaultSpan: { cols: 3, rows: 3 }, minSpan: { cols: 3, rows: 2 }, maxSpan: { cols: 6, rows: 4 } },
  }),

  'bar-chart': defineWidgetDescriptor({
    id: 'bar-chart',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Bar chart', category: 'Telemetry', titleId: 'widgetTypeBarChart', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    // One bar per measurement of a device, or (groupBy device) one bar per member of an anchor.
    binding: { members: { option: 'groupBy', equals: 'device' } },
    options: {
      title: TITLE,
      groupBy: {
        kind: 'enum',
        values: ['measurement', 'device'],
        labelId: 'widgetLabelGroupBy',
        helpId: 'widgetHintGroupBy',
        valueLabelIds: { measurement: 'widgetGroupByMeasurement', device: 'widgetGroupByDevice' },
        placeholderId: 'widgetPlaceholderDefault',
        doc: 'One bar per measurement (the default), or per device of the group for the measurement set below.',
      },
      measurement: MEASUREMENT,
      unit: UNIT,
      precision: PRECISION,
      sort: CATEGORY_SORT,
    },
    defaults: { title: 'Bar chart' },
    interactions: {},
    layout: { defaultSpan: { cols: 6, rows: 4 }, minSpan: { cols: 4, rows: 3 } },
  }),

  'pie-chart': defineWidgetDescriptor({
    id: 'pie-chart',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Pie chart', category: 'Telemetry', titleId: 'widgetTypePieChart', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ONE_DEVICE },
    binding: {},
    options: { title: TITLE, unit: UNIT, precision: PRECISION, sort: CATEGORY_SORT },
    defaults: { title: 'Pie chart' },
    interactions: {},
    layout: { defaultSpan: { cols: 4, rows: 4 }, minSpan: { cols: 3, rows: 3 }, maxSpan: { cols: 8, rows: 8 } },
  }),

  // A new widget rather than a mode of `table`: that one is one device's measurements as rows, on a
  // channel that keys its values by name; this one is a fleet, a row per member and a column per
  // measurement, and reads its data by device.
  'fleet-table': defineWidgetDescriptor({
    id: 'fleet-table',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Fleet table', category: 'Telemetry', titleId: 'widgetTypeFleetTable', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: { windowSeconds: { option: 'windowSeconds' }, members: { const: true } },
    options: {
      title: TITLE,
      columns: FLEET_COLUMNS,
      thresholds: FLEET_THRESHOLDS,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      ageLagSeconds: {
        kind: 'number',
        integer: true,
        min: 1,
        labelId: 'widgetLabelAgeLag',
        helpId: 'widgetHintAgeLag',
        doc: "Show a reading's age under it only when the reading is at least this many seconds older than the newest reading in the table (or stale). Unset shows every reading's age.",
      },
      sortBy: {
        kind: 'string',
        labelId: 'widgetLabelSortBy',
        helpId: 'widgetHintSortBy',
        doc: 'The measurement the table is ordered by. Blank orders by device name.',
      },
      sortDescending: { kind: 'boolean', labelId: 'widgetLabelSortDescending', doc: 'Largest first.' },
      pageSize: {
        kind: 'number',
        integer: true,
        min: 5,
        max: 100,
        labelId: 'widgetLabelPageSize',
        doc: 'Devices per page. Default 20.',
      },
      selectionTarget: SELECTION_TARGET,
      windowSeconds: WINDOW_SECONDS,
    },
    defaults: { title: 'Fleet table' },
    // The frame shows the age of the newest reading in the fleet; each cell shows its own.
    freshness: true,
    interactions: { selection: true },
    layout: { defaultSpan: { cols: 12, rows: 6 }, minSpan: { cols: 6, rows: 3 } },
  }),
} as const satisfies Record<BuiltinWidgetId, WidgetDescriptor>;

// The catalog the headless validators read. Constructed at module load, so a built-in that
// fails its own validation fails every consumer of this package on import — loudly, and not
// only in a test.
export const builtinWidgetCatalog = createWidgetCatalog(Object.values(builtinWidgetDescriptors));
