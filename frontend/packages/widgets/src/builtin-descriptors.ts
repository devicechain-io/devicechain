// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The portable descriptor of every built-in widget — pure data, React-free, importing no
// component. The headless validators read the catalog built from these without ever
// loading a widget; builtin-registry.ts pairs each descriptor with its component.
//
// 🔑 BUILT BY REFERENCE, NOT BY COPY. `channel` and `options` are the very values in
// WIDGET_CHANNEL and WIDGET_OPTIONS (builtin-registry.test.ts asserts identity), so the
// descriptor cannot drift from the tables the renderer and the console still read today.
// The tables stay the source of truth until the built-in cutover retires them.
//
// Every built-in carries version 1.0.0: a per-widget release number, NOT the platform
// version. It is bumped only on a rendering change worth surfacing to an author, so a
// platform release does not make every board look "authored with an older widget".

import {
  createWidgetCatalog,
  defineWidgetDescriptor,
  type BuiltinWidgetId,
  type WidgetDescriptor,
} from '@devicechain/dashboards';

import { WIDGET_OPTIONS, WIDGET_CONSTRAINTS } from './options';
import { WIDGET_CHANNEL } from './registry';

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
    display: { title: 'Timeseries chart', category: 'Telemetry' },
    channel: WIDGET_CHANNEL['timeseries-chart'],
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: { window: { option: 'window', min: 1 } },
    options: WIDGET_OPTIONS['timeseries-chart'],
    defaults: { title: 'Timeseries chart' },
    interactions: {},
  }),

  'latest-card': defineWidgetDescriptor({
    id: 'latest-card',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Latest card', category: 'Telemetry' },
    channel: WIDGET_CHANNEL['latest-card'],
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: { window: { option: 'window', min: 1 } },
    options: WIDGET_OPTIONS['latest-card'],
    defaults: { title: 'Latest card' },
    interactions: {},
  }),

  gauge: defineWidgetDescriptor({
    id: 'gauge',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Gauge', category: 'Telemetry' },
    channel: WIDGET_CHANNEL.gauge,
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: { window: { option: 'window', min: 1 } },
    options: WIDGET_OPTIONS.gauge,
    constraints: WIDGET_CONSTRAINTS.gauge,
    defaults: { title: 'Gauge' },
    interactions: {},
  }),

  table: defineWidgetDescriptor({
    id: 'table',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Table', category: 'Telemetry' },
    channel: WIDGET_CHANNEL.table,
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: { window: { option: 'window', min: 1 } },
    options: WIDGET_OPTIONS.table,
    defaults: { title: 'Table' },
    interactions: {},
  }),

  // label and image sit on the measurement channel today only because that is
  // ConnectedWidget's default branch; they bind no datasource and read no stream, which the
  // datasource mode says. A `none` channel retires the pretence in the built-in cutover.
  label: defineWidgetDescriptor({
    id: 'label',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Label', category: 'Static' },
    channel: WIDGET_CHANNEL.label,
    datasource: NO_DATASOURCE,
    binding: {},
    options: WIDGET_OPTIONS.label,
    defaults: { text: 'New label' },
    interactions: {},
  }),

  image: defineWidgetDescriptor({
    id: 'image',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Image', category: 'Static' },
    channel: WIDGET_CHANNEL.image,
    datasource: NO_DATASOURCE,
    binding: {},
    options: WIDGET_OPTIONS.image,
    defaults: { title: 'Image' },
    interactions: {},
  }),

  'alarm-table': defineWidgetDescriptor({
    id: 'alarm-table',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Alarm table', category: 'Alarms' },
    channel: WIDGET_CHANNEL['alarm-table'],
    datasource: { mode: 'optional', selectors: ALL_SELECTORS },
    binding: { pageSize: { option: 'maxRows', default: 50, min: 1 }, ...ALARM_FILTERS },
    options: WIDGET_OPTIONS['alarm-table'],
    defaults: { title: 'Alarm table', state: 'ACTIVE' },
    interactions: { alarmActions: true, selection: true },
  }),

  // The count shows the SERVER total and reads a page only to sample the worst loaded
  // severity, so its page size is a constant rather than an authorable option.
  'alarm-count': defineWidgetDescriptor({
    id: 'alarm-count',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Alarm count', category: 'Alarms' },
    channel: WIDGET_CHANNEL['alarm-count'],
    datasource: { mode: 'optional', selectors: ALL_SELECTORS },
    binding: { pageSize: { const: 50 }, ...ALARM_FILTERS },
    options: WIDGET_OPTIONS['alarm-count'],
    defaults: { title: 'Alarm count', state: 'ACTIVE' },
    interactions: {},
  }),

  'command-button': defineWidgetDescriptor({
    id: 'command-button',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Command button', category: 'Control' },
    channel: WIDGET_CHANNEL['command-button'],
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { pageSize: { option: 'maxRows', default: 20, min: 1 } },
    options: WIDGET_OPTIONS['command-button'],
    defaults: { title: 'Command button' },
    interactions: { commandSend: true },
  }),

  'entity-selector': defineWidgetDescriptor({
    id: 'entity-selector',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Entity selector', category: 'Navigation' },
    channel: WIDGET_CHANNEL['entity-selector'],
    datasource: NO_DATASOURCE,
    binding: {},
    options: WIDGET_OPTIONS['entity-selector'],
    defaults: { title: 'Entity selector' },
    interactions: { selection: true, candidates: true },
  }),

  map: defineWidgetDescriptor({
    id: 'map',
    version: VERSION,
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Map', category: 'Location' },
    channel: WIDGET_CHANNEL.map,
    datasource: { mode: 'required', selectors: ALL_SELECTORS },
    binding: {},
    options: WIDGET_OPTIONS.map,
    defaults: { title: 'Map' },
    interactions: {},
  }),
} as const satisfies Record<BuiltinWidgetId, WidgetDescriptor>;

// The catalog the headless validators read. Constructed at module load, so a built-in that
// fails its own validation fails every consumer of this package on import — loudly, and not
// only in a test.
export const builtinWidgetCatalog = createWidgetCatalog(Object.values(builtinWidgetDescriptors));
