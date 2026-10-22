// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The authoring documentation of every built-in widget option -- the `doc` text each option spec
// carries for the config panel's help and the published "available widgets" page.
//
// 🔴 WHY THIS IS NOT IN builtin-descriptors.ts. A `doc` is read by nothing that RENDERS a board,
// only by authoring surfaces, yet it is prose, and prose is the most expensive thing per byte that
// a descriptor carries: kept inline it shipped in the first download of every host (the replay
// player's was ~2 KB gzip, growing with each widget). So the runtime descriptors
// (builtin-descriptors.ts) carry none, and `builtinWidgetDescriptors` / `builtinWidgetCatalog` as
// PUBLIC exports are rebuilt from them plus this table (documented-descriptors.ts), which a host that
// never imports those two drops from its bundle.
//
// Keys are option PATHS: `thresholds` for an option, `thresholds.value` for a list option's item
// field. `OPTION_DOCS` is the text an option has on most widgets; `OPTION_DOC_OVERRIDES` is where a
// widget words one differently. A built-in option with no doc here fails
// documented-descriptors.test.ts, which is where the "every option is documented" rule that
// validateWidgetDescriptor applies to extensions is enforced for the built-ins.

export const OPTION_DOCS: Readonly<Record<string, string>> = {
  title: 'Heading shown in the widget frame. Blank hides the frame heading.',
  unit: 'Unit suffix shown beside the value.',
  precision: 'Decimal places to round displayed values to. Unset rounds to at most 3 decimals (4 significant digits under 1).',
  thresholds: 'Severity bands over the value. Each entry starts a band at its value that runs to the next entry; below the first, and in a normal band, the widget keeps its own colour.',
  'thresholds.value': 'The value at which this band starts.',
  'thresholds.level': 'The severity of this band. The theme decides the colour; normal has none.',
  'thresholds.label': 'Text shown on the chart line for this threshold. Defaults to its value.',
  staleAfterSeconds: 'Seconds after which the newest sample is called stale: the value is greyed out and badged. Unset shows the sample age only and never marks the widget stale.',
  windowSeconds: 'Seconds of history the widget covers, back from now. Leave unset to follow the board’s time range; set it to pin this widget to a fixed span that the viewer’s range picker does not change. 60 to 604800.',
  measurement: 'Which measurement to display. Defaults to the first the datasource selects.',
  flashOnChange: 'Briefly tint a value green on a rise and red on a fall when it changes.',
  window: "Samples retained across ALL of the widget's measurements before the oldest are dropped. Bounds memory; only the chart draws them. Default 300.",
  min: 'Value at the empty end of the scale. Required: the widget does not guess a range.',
  max: 'Value at the full end of the scale. Required, and above min.',
  text: 'The text to show. Rendered as plain text — markup is never interpreted.',
  align: 'Horizontal alignment of the text. Default center.',
  fontSize: 'Text size in pixels at the comfortable density; it scales with the board density. Default 16.',
  url: 'Source URL of the image.',
  alt: 'Alternative text describing the image for assistive technology.',
  fit: 'How the image fills its box. Default contain.',
  state: 'Show only alarms in this state. Unset shows every state.',
  severity: 'Show only alarms at this severity. Unset shows every severity.',
  acknowledged: "Show only acknowledged ('true') or unacknowledged ('false') alarms. Unset shows both.",
  groupBy: 'deviceType: a tab per device type.',
  deviceType: 'Only alarms on devices of this type (token).',
  deviceTypes: 'Types to tab, in order; unset tabs all.',
  'deviceTypes.token': 'Type token.',
  maxRows: 'Alarms loaded into the table. Default 50.',
  selectionTarget: 'Name of the slot this widget re-points when the viewer picks an entity.',
  commandName: 'Command key issued to the target device. Without it the control cannot send.',
  commandLabel: 'Name of the control for assistive technology. Defaults to the command key.',
  parameterSchema: "The command's parameter descriptors, baked in as JSON at author time. Gives the control its range and its refusals.",
  tileUrl: "Raster tile URL template, e.g. https://tiles.example.com/{z}/{x}/{y}.png. Leave empty to inherit the tenant's basemap; setting one overrides it for this board only.",
  attribution: 'Credit line the tile source requires, shown on the map. Set whatever the provider you chose asks for.',
  style: 'A plain bar (default) or a thermometer: a vertical tube with a bulb. A thermometer is always vertical.',
  markAlarms: 'Mark this device’s alarms on the chart, from raise to clear.',
  markAlarmsScope: 'Which alarms to mark: only those about what this chart plots (series, the default for a new chart), or every alarm on the device. An alarm with no known measurement is never marked in series mode; it is listed under the chart as another alarm on the device. A chart saved before this option marks the whole device.',
  markCommands: 'Mark the commands sent to this device on the chart.',
  orientation: 'Direction of a bar: horizontal (default) or vertical. Ignored by a thermometer.',
  readout: 'What the readout shows: the value with its unit (default) or the percent of the scale.',
  onAt: 'For an on/off signal: the lamp is lit at or above this value and dark below it. Unset keeps the lamp lit while a value is present, coloured by its threshold band.',
  parameter: 'Which parameter of the command this control sets. May be left blank when the command has exactly one parameter of the right type.',
  step: 'Distance between values. Defaults to 1 for a whole-number parameter, else a hundredth of the range.',
  mode: 'Slider (default) or stepper: − / + and Send.',
  stat: 'Which figure to show. Default average.',
  sparkline: 'Draw the measurement’s trend over the span under the figure. Default off.',
  sort: 'Order of the categories. Default: as the data source lists them.',
  columns: 'Heading, unit and precision per measurement column; a column with none shows its name.',
  'columns.measurement': 'The measurement column this entry applies to.',
  'columns.label': 'Column heading. Defaults to the measurement name.',
  'columns.unit': 'Unit suffix shown beside the value.',
  'columns.precision': 'Decimal places to round displayed values to. Unset rounds to at most 3 decimals (4 significant digits under 1).',
  'thresholds.measurement': 'The measurement column this entry applies to.',
  ageLagSeconds: "Show a reading's age under it only when the reading is at least this many seconds older than the newest reading in the table (or stale). Unset shows every reading's age.",
  sortBy: 'The measurement the table is ordered by. Blank orders by device name.',
  sortDescending: 'Largest first.',
  pageSize: 'Devices per page. Default 20.',
  maxSeries: 'Most lines drawn when the chart shows one line per device. Default 5, at most 12. Each line costs one stored-history read, and the chart colours repeat after five; a group larger than this shows the first devices of the group and says how many it left out.',
  show: 'What to count: devices in an abnormal band of the measurement (default), or all devices.',
};

export const OPTION_DOC_OVERRIDES: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  'latest-card': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
  },
  gauge: {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
    min: 'Low end of the gauge scale. Default 0.',
    max: 'High end of the gauge scale. Default 100.',
  },
  'linear-gauge': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
  },
  'level-gauge': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
  },
  battery: {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
    min: 'Value of an empty battery. Default 0 (the scale is percent unless you set min and max).',
    max: 'Value of a full battery. Default 100 (the scale is percent unless you set min and max).',
  },
  'status-light': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
  },
  'signal-bars': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
    thresholds: 'The steps of the signal, lowest first: one bar per entry, lit once the value reaches it and coloured by its level. At least two, at most eight.',
  },
  compass: {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
    precision: 'Decimal places of the heading. Default 0 (whole degrees). The heading is rounded first, then folded onto 0 to 360, so it never reads 360.',
    thresholds: 'Severity bands over the heading in degrees (0 to 360, after folding). Each entry starts a band at its value that runs to the next entry; below the first, and in a normal band, the needle keeps its own colour.',
  },
  'progress-ring': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
    readout: 'What the middle of the ring shows: the percent of the scale (default) or the value with its unit.',
  },
  'stat-card': {
    title: 'Heading shown in the widget frame. Defaults to the displayed measurement name.',
  },
  'fleet-count': {
    measurement: 'The measurement whose bands flag a device. Defaults to the first the datasource selects.',
  },
  'command-slider': {
    min: "Lowest value, used only when the command's parameter declares no minimum.",
    max: "Highest value, used only when the command's parameter declares no maximum.",
  },
  'timeseries-chart': {
    min: 'Low end of the value axis. Unset, the axis starts where the chart picks. A reading or threshold below it still shows: the axis widens to take it in.',
    max: 'High end of the value axis, above min. Unset, the chart picks. A reading or threshold above it still shows: the axis widens to take it in.',
    groupBy: 'One line per measurement of one device (the default), or one line per device of a group for the measurement set below. Per device, stored history is drawn as the average of each time bucket (the chart says so), and thresholds are reference lines over it.',
    measurement: 'The measurement compared across the group. Defaults to the first the datasource selects. Used only with one line per device.',
    markAlarms: 'Mark this device’s alarms on the chart, from raise to clear. Not drawn with one line per device.',
    markCommands: 'Mark the commands sent to this device on the chart. Not drawn with one line per device.',
  },
  'bar-chart': {
    groupBy: 'One bar per measurement (the default), or per device of the group for the measurement set below.',
  },
  'command-button': {
    maxRows: 'Recent commands shown in the history. Default 20.',
    commandName: 'Command key issued to the target device. Without it the widget cannot send.',
    commandLabel: 'Text on the send button. Defaults to the command key.',
    parameterSchema: "The command's parameter descriptors, baked in as JSON at author time. Drives the typed form.",
  },
};
