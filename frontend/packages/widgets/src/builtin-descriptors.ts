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
  doc: 'Decimal places to round displayed values to. Unset shows the raw value.',
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


// Each widget's `layout` is its size contract, in cells of the CURRENT 24-column grid (rows
// are 40 px, gutters 8 px). The minimums are the smallest box in which the widget's parts
// all fit with room to spare: rows are the binding constraint, since the frame header costs
// ~34-50 px before the widget's own content. The defaults are what a freshly added widget
// takes. A maximum is declared only where the content stops filling the box (a single value,
// a dial, a button); a chart, table, map or image fills a whole wall, so it has none.
// 🔴 Square, constant-size cells re-calibrate every number below; the shape stays.

const VERSION = '1.0.0';
const ALL_SELECTORS = ['device', 'anchor', 'slot'] as const;
const NO_DATASOURCE = { mode: 'none' } as const;

// The alarm filters are read by both alarm widgets from the same option keys.
const ALARM_FILTERS = {
  state: { option: 'state' },
  severity: { option: 'severity' },
  acknowledged: { option: 'acknowledged' },
} as const;

export const builtinWidgetDescriptors = {
  'timeseries-chart': defineWidgetDescriptor({
    id: 'timeseries-chart',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Timeseries chart', category: 'Telemetry', titleId: 'widgetTypeTimeseriesChart', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      title: TITLE,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Timeseries chart' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 12, rows: 6 }, minSpan: { cols: 8, rows: 4 } },
  }),

  'latest-card': defineWidgetDescriptor({
    id: 'latest-card',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Latest card', category: 'Telemetry', titleId: 'widgetTypeLatestCard', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
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
      flashOnChange: FLASH_ON_CHANGE,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    defaults: { title: 'Latest card' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 8, rows: 3 }, minSpan: { cols: 6, rows: 3 }, maxSpan: { cols: 12, rows: 8 } },
  }),

  gauge: defineWidgetDescriptor({
    id: 'gauge',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Gauge', category: 'Telemetry', titleId: 'widgetTypeGauge', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
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
      flashOnChange: FLASH_ON_CHANGE,
      staleAfterSeconds: STALE_AFTER_SECONDS,
      window: WINDOW,
    },
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Gauge' },
    interactions: {},
    freshness: true,
    layout: { defaultSpan: { cols: 8, rows: 4 }, minSpan: { cols: 6, rows: 3 }, maxSpan: { cols: 12, rows: 8 } },
  }),

  table: defineWidgetDescriptor({
    id: 'table',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Table', category: 'Telemetry', titleId: 'widgetTypeTable', categoryId: 'widgetCategoryTelemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
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
    layout: { defaultSpan: { cols: 12, rows: 5 }, minSpan: { cols: 6, rows: 3 } },
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
        doc: 'Text size in pixels. Default 16.',
      },
    },
    defaults: { text: 'New label' },
    interactions: {},
    layout: { defaultSpan: { cols: 6, rows: 2 }, minSpan: { cols: 2, rows: 1 }, maxSpan: { cols: 24, rows: 4 } },
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
    layout: { defaultSpan: { cols: 6, rows: 4 }, minSpan: { cols: 2, rows: 2 } },
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
    layout: { defaultSpan: { cols: 12, rows: 6 }, minSpan: { cols: 8, rows: 3 } },
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
    layout: { defaultSpan: { cols: 6, rows: 3 }, minSpan: { cols: 4, rows: 3 }, maxSpan: { cols: 12, rows: 6 } },
  }),

  'command-button': defineWidgetDescriptor({
    id: 'command-button',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Command button', category: 'Control', titleId: 'widgetTypeCommandButton', categoryId: 'widgetCategoryControl' },
    channel: 'control',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
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
    layout: { defaultSpan: { cols: 6, rows: 3 }, minSpan: { cols: 5, rows: 3 }, maxSpan: { cols: 12, rows: 8 } },
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
    layout: { defaultSpan: { cols: 6, rows: 2 }, minSpan: { cols: 4, rows: 2 }, maxSpan: { cols: 12, rows: 2 }, resize: 'horizontal' },
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
    },
    defaults: { title: 'Map' },
    interactions: {},
    layout: { defaultSpan: { cols: 12, rows: 8 }, minSpan: { cols: 6, rows: 4 } },
  }),
} as const satisfies Record<BuiltinWidgetId, WidgetDescriptor>;

// The catalog the headless validators read. Constructed at module load, so a built-in that
// fails its own validation fails every consumer of this package on import — loudly, and not
// only in a test.
export const builtinWidgetCatalog = createWidgetCatalog(Object.values(builtinWidgetDescriptors));
