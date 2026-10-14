// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/widget-sdk — the supported surface for writing a dashboard widget.
//
// A widget is declared once with `defineWidget` (a portable, JSON-only descriptor plus a
// trusted React component) and made available to a host by listing it in
// `createWidgetRegistry`. The built-in widgets are declared through exactly this
// interface. The descriptor vocabulary is React-free and lives in @devicechain/dashboards;
// it is re-exported here so a widget package imports from one place.

export { defineWidget } from './define-widget';
export type { ErasedComponent, ErasedWidgetDefinition, WidgetDefinition, WidgetImplementation } from './define-widget';
export { createWidgetRegistry, WidgetRegistryError, type WidgetRegistry } from './registry';
export type {
  EditorCommandChoice,
  EditorCommandChoices,
  WidgetComponent,
  WidgetEditorProps,
  WidgetEditorServices,
  WidgetProps,
} from './widget';

// The ambient host contexts (selection, candidates, time zone, theme root, subject). Defined
// in this package and nowhere else — see ./context.
export {
  WidgetCandidatesProvider,
  WidgetSelectProvider,
  WidgetSubjectProvider,
  WidgetThemeRootProvider,
  WidgetTimeZoneProvider,
  useWidgetCandidates,
  useWidgetSelect,
  useWidgetSubject,
  useWidgetThemeRoot,
  useWidgetTimeZone,
  type WidgetCandidates,
  type WidgetSelect,
} from './context';

// The building blocks every widget (built-in or third-party) composes from: the card frame,
// theme-variable references, number/time formatting, the opt-in change-flash cue, option
// readers, and the React bindings between the hub's channels and widget state. The chart
// engine helpers are NOT here -- they are on the `/echarts` subpath, so importing this root
// never pulls Apache ECharts (nor MapLibre, nor the built-in catalog).
export { WidgetFrame, type WidgetFrameProps } from './frame';
export { css, type ThemeVar } from './theme';
export { fitValueFont } from './fit-text';
export { formatDateTime, formatTimestamp, formatValue } from './format';
export {
  FlashValue,
  flashBackgroundStyle,
  flashTextStyle,
  useFlashOnChange,
  type FlashDirection,
} from './flash';
export { optBoolean, optNumber, optString, pickSample, primaryMeasurementName } from './widget-options';
export {
  useAlarmStream,
  useCandidates,
  useCommandStream,
  useDashboardHub,
  useDatasourceAvailability,
  useElementSize,
  useLocationStream,
  useMeasurementStream,
  useResolvedBindings,
  useSlotCandidates,
  type AlarmStreamState,
  type CandidatesState,
  type CommandStreamState,
  type DatasourceAvailability,
  type ElementSize,
  type LocationStreamState,
  type MeasurementStreamOptions,
  type MeasurementStreamState,
} from './hooks';

// The portable descriptor vocabulary.
export {
  SDK_API_VERSION,
  WidgetCatalogError,
  defineWidgetDescriptor,
  isBuiltinWidgetId,
  isNamespacedWidgetId,
  validateWidgetDescriptor,
  type AckOptionRef,
  type BooleanOptionSpec,
  type BuiltinWidgetId,
  type ChannelBindingPolicy,
  type ChannelSnapshot,
  type ConstBinding,
  type DatasourcePolicy,
  type DatasourceSelectorKind,
  type DefaultsOf,
  type DescriptorIssue,
  type EnumOptionSpec,
  type GreaterThanConstraint,
  type NamespacedWidgetId,
  type NumberOptionRef,
  type NumberOptionSpec,
  type OptionConstraint,
  type OptionSpec,
  type OptionsOf,
  type StringOptionRef,
  type StringOptionSpec,
  type WidgetCatalog,
  type WidgetChannel,
  type WidgetDescriptor,
  type WidgetDisplay,
  type WidgetId,
  type WidgetInteractionDeclaration,
  type WidgetOptionSpecs,
} from '@devicechain/dashboards';
