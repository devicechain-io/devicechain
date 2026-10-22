// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The portable half of a widget registration: a plain-data DESCRIPTOR naming what a widget
// is, which data channel feeds it, what options it owns and how its data is bound. A
// descriptor holds no React component, callback, import URL, token or executable code —
// it is JSON-serializable by construction (and checked to be so at runtime, see
// validateWidgetDescriptor), because the definition parser and the headless validators
// must be able to read one without loading, or trusting, any widget implementation.
//
// The trusted React implementation that goes with a descriptor lives in
// @devicechain/widget-sdk; this module is the React-free vocabulary both sides share.

import type {
  AggregateStreamState,
  AlarmStreamState,
  CommandStreamState,
  LocationStreamState,
  MeasurementStreamState,
} from './channel-snapshots';
import type { DefaultsOf, OptionConstraint, OptionKeysOfKind, WidgetOptionSpecs } from './option-schema';
import { WIDGET_TYPES, type WidgetType } from './types';

// ---- Channels ---------------------------------------------------------------

// A widget's data channel: which hub subscription binds it. `selection` and `none` open no
// data channel at all — a selection widget drives a slot from the ambient candidate
// provider, and a `none` widget is static.
export type WidgetChannel = 'measurement' | 'alarm' | 'control' | 'selection' | 'location' | 'aggregate' | 'none';

// The resolved data a component on channel C receives. `null` marks the empty channel.
export type ChannelSnapshot<C extends WidgetChannel> = C extends 'measurement'
  ? MeasurementStreamState
  : C extends 'alarm'
    ? AlarmStreamState
    : C extends 'control'
      ? CommandStreamState
      : C extends 'location'
        ? LocationStreamState
        : C extends 'aggregate'
          ? AggregateStreamState
          : null;

// ---- Ids --------------------------------------------------------------------

export type BuiltinWidgetId = WidgetType;
// `scope/name`, e.g. 'acme/tank-level'. Namespace ownership is an operator convention, not
// proof of publisher identity.
export type NamespacedWidgetId = `${string}/${string}`;
export type WidgetId = BuiltinWidgetId | NamespacedWidgetId;

const BUILTIN_IDS: ReadonlySet<string> = new Set(WIDGET_TYPES);

export function isBuiltinWidgetId(id: unknown): id is BuiltinWidgetId {
  return typeof id === 'string' && BUILTIN_IDS.has(id);
}

const NAMESPACED_ID = /^[a-z0-9][a-z0-9-]*\/[a-z0-9][a-z0-9-]*$/;

export function isNamespacedWidgetId(id: unknown): id is NamespacedWidgetId {
  return typeof id === 'string' && NAMESPACED_ID.test(id);
}

// ---- Datasource -------------------------------------------------------------

export type DatasourceSelectorKind = 'device' | 'anchor' | 'slot';

// Whether a widget binds a datasource, and which selector kinds its author may pick.
// `none`/`selection` widgets must be `{mode:'none'}`; a control widget's command target
// keeps its device-only requirement, so it offers only device and slot.
export type DatasourcePolicy =
  | { mode: 'none' }
  | { mode: 'optional' | 'required'; selectors: readonly DatasourceSelectorKind[] };

type ControlDatasourcePolicy =
  | { mode: 'none' }
  | { mode: 'optional' | 'required'; selectors: readonly ('device' | 'slot')[] };

export type DatasourcePolicyFor<C extends WidgetChannel> = C extends 'none' | 'selection'
  ? { mode: 'none' }
  : C extends 'control'
    ? ControlDatasourcePolicy
    : DatasourcePolicy;

// ---- Binding policy ---------------------------------------------------------

// A constant, or a reference to a declared option key. References are by NAME and checked
// against the option's kind — the descriptor never carries a function or a query string.
export interface ConstBinding<T> {
  const: T;
}

export interface NumberOptionRef<S extends WidgetOptionSpecs = WidgetOptionSpecs> {
  option: OptionKeysOfKind<S, 'number'>;
  default?: number;
  min?: number;
}

export interface StringOptionRef<S extends WidgetOptionSpecs = WidgetOptionSpecs> {
  option: OptionKeysOfKind<S, 'string' | 'enum'>;
  default?: string;
}

// The alarm acknowledged filter is the STRING 'true'/'false' (see the built-in
// `acknowledged` option), so it references an enum option whose legal values are a subset of
// those two. The kind is checked at compile time; the value set at registration.
export interface AckOptionRef<S extends WidgetOptionSpecs = WidgetOptionSpecs> {
  option: OptionKeysOfKind<S, 'enum'>;
  default?: 'true' | 'false';
}

type PageSize<S extends WidgetOptionSpecs> = ConstBinding<number> | NumberOptionRef<S>;

export type ChannelBindingPolicy<C extends WidgetChannel, S extends WidgetOptionSpecs = WidgetOptionSpecs> =
  C extends 'aggregate'
    ? {
        // The span the aggregates cover: the option pins it (seconds); unset, the board's range.
        windowSeconds?: NumberOptionRef<S>;
      }
    : C extends 'measurement'
    ? {
        window?: NumberOptionRef<S>;
        // Declaring it makes the widget TIME-RANGED: it draws a span rather than a sample count.
        // The referenced option pins the span (seconds); unset, the widget follows the board's
        // time range, which a viewer may change.
        windowSeconds?: NumberOptionRef<S>;
        // Makes the widget a FLEET widget: its datasource is read member by member (one row or bar
        // per device of an anchor) and `data.members` is set. `{ const: true }` always; or an option
        // reference with the value that turns the mode on (a bar chart's "one bar per device").
        members?: ConstBinding<true> | (StringOptionRef<S> & { equals: string });
        // Makes the widget a PRESENCE widget: it reads its devices' connectivity (online or offline,
        // and when each was last active) rather than their readings, and `data.members` is set with
        // each member's `presence`. `{ const: true }` only.
        presence?: ConstBinding<true>;
      }
    : C extends 'alarm'
      ? {
          pageSize: PageSize<S>;
          state?: StringOptionRef<S>;
          severity?: StringOptionRef<S>;
          acknowledged?: AckOptionRef<S>;
          // Makes the widget group its alarms by the originating device's type (a tab strip):
          // the referenced option holding `equals` turns the mode on. `data.typeTabs` is then set.
          groupBy?: StringOptionRef<S> & { equals: string };
          // Makes the widget read the per-severity COUNTS of its alarms instead of alarm rows:
          // `data.severityCounts` is set and `data.alarms` is empty. `{ const: true }` only.
          countBySeverity?: ConstBinding<true>;
          // Only alarms on devices of the type (token) the referenced option holds; unset, all.
          deviceType?: StringOptionRef<S>;
        }
      : C extends 'control'
        ? { pageSize: PageSize<S> }
        : Record<string, never>;

// ---- Interactions -----------------------------------------------------------

// The host callbacks a widget asks for. Declaring one is a request, intersected with what
// the host supports and the viewer is allowed — not a security boundary.
export interface WidgetInteractionDeclaration {
  alarmActions?: true;
  commandSend?: true;
  selection?: true;
  candidates?: true;
}

// `alarmActions` only makes sense on the alarm channel and `commandSend` only on control.
// `'alarm' extends C` (rather than `C extends 'alarm'`) keeps the broad, unparameterized
// descriptor able to name every key.
export type InteractionsFor<C extends WidgetChannel> = { selection?: true; candidates?: true } & ('alarm' extends C
  ? { alarmActions?: true }
  : unknown) &
  ('control' extends C ? { commandSend?: true } : unknown);

// ---- Display ----------------------------------------------------------------

export interface WidgetDisplay {
  title: string;
  description?: string;
  category?: string;
  // Message ids a host may resolve in its own catalog before falling back to the text above.
  titleId?: string;
  categoryId?: string;
}

// ---- Size -------------------------------------------------------------------

// A size in grid CELLS: columns across, rows down.
export interface WidgetSpan {
  cols: number;
  rows: number;
}

// Which axes a viewer-author may resize. 'both' is the default.
export type WidgetResizeMode = 'both' | 'horizontal' | 'vertical' | 'none';

// How big a widget is allowed to be, declared by the widget and checked by the layout lint.
// Plain JSON, validated when the catalog is built.
//
// Spans are counted in SQUARE cells, never smaller than the board's minCellSize (56 px by
// default), so a minSpan sized at that cell is readable at every width the layout renders at.
//
// 🔑 The move to square cells did NOT bump SDK_API_VERSION, deliberately. The numbers here
// changed meaning (they were cells of a 24-column grid with 40 px rows, roughly twice as many
// columns), but the block is advisory size metadata: the editor and the lint read it, and
// nothing renders from it. A package still declaring the old numbers keeps rendering, and a
// board placing it outside them is a lint error, which is the right size of failure. A bump would
// instead turn every stored widget of every extension into an unresolved placeholder, and refuse
// every descriptor, until each package was re-released: a hard break for a soft change.
export interface WidgetLayoutSpec {
  // The size a freshly added widget takes.
  defaultSpan: WidgetSpan;
  // The resize floor. Absent means 1 x 1.
  minSpan?: WidgetSpan;
  // The resize ceiling. Absent means none.
  maxSpan?: WidgetSpan;
  // Absent means 'both'. A restricted mode PINS the other axis at defaultSpan: 'horizontal'
  // pins rows, 'vertical' pins columns, 'none' pins both. A minSpan or maxSpan, if declared,
  // must agree with defaultSpan on a pinned axis; neither is needed to pin it.
  resize?: WidgetResizeMode;
}

// The axes a resize mode holds at defaultSpan.
export function pinnedAxes(mode: WidgetResizeMode | undefined): readonly ('cols' | 'rows')[] {
  switch (mode) {
    case 'horizontal':
      return ['rows'];
    case 'vertical':
      return ['cols'];
    case 'none':
      return ['cols', 'rows'];
    default:
      return [];
  }
}

// Third-party widgets that declare no `layout` are added at this size, with no limits.
export const FALLBACK_DEFAULT_SPAN: Readonly<WidgetSpan> = Object.freeze({ cols: 4, rows: 2 });

// ---- The descriptor ---------------------------------------------------------

export const SDK_API_VERSION = 1;

export interface WidgetDescriptor<
  C extends WidgetChannel = WidgetChannel,
  S extends WidgetOptionSpecs = WidgetOptionSpecs,
> {
  id: WidgetId;
  // Strict MAJOR.MINOR.PATCH of this widget's release. Provenance, not a compatibility
  // key: a differing version still renders.
  version: string;
  // The contract generation, independent of platform and widget versions.
  sdkApiVersion: 1;
  // The persisted settings shape. Bump when stored options stop meaning the same thing.
  optionsVersion: number;
  display: WidgetDisplay;
  channel: C;
  datasource: DatasourcePolicyFor<C>;
  binding: ChannelBindingPolicy<C, S>;
  // The option schema this widget owns.
  options: S;
  // Rules spanning more than one option, as data.
  constraints?: readonly OptionConstraint<OptionKeysOfKind<S, 'number'>>[];
  // Starting values for a freshly added widget. Every key optional — see DefaultsOf.
  defaults: DefaultsOf<S>;
  interactions: InteractionsFor<C>;
  // Opt in to the frame's age readout ("12 sec. ago") for the widget's newest sample, and to
  // the stale treatment when the widget's `staleAfterSeconds` option sets a threshold. Only a
  // measurement widget has samples to age. Optional and absent by default, so a descriptor
  // that predates it, and every stored widget, is unchanged.
  freshness?: true;
  // Size contract (see WidgetLayoutSpec). Required of built-ins by a registry test; optional for
  // a third-party widget, which then gets FALLBACK_DEFAULT_SPAN and no limits.
  layout?: WidgetLayoutSpec;
}

// defineWidgetDescriptor is an identity that exists for inference: it infers the channel
// and the schema from the literal, so a binding reference, a constraint or a default that
// disagrees with the schema is a compile error. The registry-facing helper for a full
// widget (descriptor + component) is `defineWidget` in @devicechain/widget-sdk.
export function defineWidgetDescriptor<const C extends WidgetChannel, const S extends WidgetOptionSpecs>(
  descriptor: WidgetDescriptor<C, S>,
): WidgetDescriptor<C, S> {
  return descriptor;
}
