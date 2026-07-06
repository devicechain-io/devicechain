// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboard-definition contract (ADR-039). These types are the canonical
// shape of the JSON document that dashboard-management stores opaquely — the
// backend only validates it is well-formed JSON, so this package owns the shape.
// Kept deliberately fluid pre-GA. See research/dashboard-phase1-design-2026-07.md.

// ---- Datasource selectors ---------------------------------------------------
//
// A selector is a tagged union (discriminated on `kind`). The Hub resolves
// `device`, `anchor`, and `slot` (the last via its binding manifest); the
// remaining kinds are reserved — present so definitions stay forward-compatible,
// but the Hub rejects them until implemented.

// device — one device's measurements (latest-card, gauge, single-series chart).
export interface DeviceSelector {
  kind: 'device';
  deviceToken: string;
  measurements: string[];
}

// The dimension a dashboard aggregates over — a tracked relationship to a
// customer / area / asset. anchors beat ThingsBoard's opaque aliases: they are
// the graph edges the platform already resolves events against.
export interface AnchorTarget {
  relationship: string;
  targetType: 'customer' | 'area' | 'asset';
  targetToken: string;
}

export interface AnchorAggregation {
  window: string; // e.g. '1m'
  fn: 'avg' | 'min' | 'max' | 'sum' | 'count';
}

// anchor — an anchor dimension aggregated over its member devices.
export interface AnchorSelector {
  kind: 'anchor';
  anchor: AnchorTarget;
  measurements: string[];
  // RESERVED (Phase 2): server-side aggregation of the member devices into one
  // series. Phase 1 resolves an anchor to its member devices and streams each
  // one's raw samples; the Hub does NOT read this field yet. Present so a
  // definition authored against the final shape still round-trips.
  aggregation?: AnchorAggregation;
}

// ---- Reserved selectors (Phase 2) -------------------------------------------
// Schema-valid and part of the union so a stored definition never fails to parse,
// but the Hub throws "not supported yet" until Phase 2 wires them.

export interface DevicesSelector {
  kind: 'devices';
  deviceTokens: string[];
  measurements: string[];
}

export interface RelatedTraversalSelector {
  kind: 'relatedTraversal';
  from: string;
  relationship: string;
  measurements: string[];
}

export interface EntityFromStateSelector {
  kind: 'entityFromState';
  stateKey: string;
  measurements: string[];
}

// slot — a NAMED reference into the dashboard's `slots` section, resolved to a
// concrete entity at MOUNT by the host's binding manifest (ADR-039 runtime
// binding). This makes a definition a reusable TEMPLATE: the widget names the
// entity role (`slot`) and the measurements it wants, and two mounts of the same
// definition can bind the slot to two different devices. The Hub resolves it via
// the binding manifest (a slot's defaultBinding, overridable by the host); an
// unbound slot renders as an empty placeholder.
export interface SlotSelector {
  kind: 'slot';
  slot: string;
  measurements: string[];
}

export type DatasourceSelector =
  | DeviceSelector
  | AnchorSelector
  | DevicesSelector
  | RelatedTraversalSelector
  | EntityFromStateSelector
  | SlotSelector;

// ---- Canvas + widgets -------------------------------------------------------

// The built-in widget types. Kept as a runtime array so parse/validation and the
// widget registry share one source of truth (WidgetType is derived from it).
//
// Widgets fall into data CHANNELS (see WIDGET_CHANNEL in @devicechain/widgets): the
// measurement widgets stream telemetry samples; the alarm widgets consume the raised-
// alarm surface (ADR-041) via the hub's alarm channel. A widget's channel decides
// which hook/registry the renderer binds it through.
export const WIDGET_TYPES = [
  'timeseries-chart',
  'latest-card',
  'gauge',
  'table',
  'label',
  'image',
  'alarm-table',
  'alarm-count',
] as const;

export type WidgetType = (typeof WIDGET_TYPES)[number];

// Absolute placement + z-order — canvas-first layout (layering native, the
// ThingsBoard default-layout gap we set out to beat).
export interface WidgetBox {
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
}

// Per-breakpoint placement, keyed by breakpoint name. 'base' is required; a
// widget omitting a breakpoint inherits its 'base' box.
export type WidgetLayout = Record<string, WidgetBox>;

export interface WidgetInstance {
  id: string;
  type: WidgetType;
  layout: WidgetLayout;
  // label/image widgets carry no datasource.
  datasource?: DatasourceSelector;
  // Widget-specific options (series colors, unit, thresholds, …). Owned by the
  // widget package; opaque here.
  options?: Record<string, unknown>;
}

export interface CanvasBackground {
  color?: string | null;
  imageUrl?: string | null;
}

export interface CanvasGrid {
  snap: boolean;
  size: number;
}

// Breakpoint name → min viewport width in px. 'base' is required.
export type Breakpoints = Record<string, number>;

export interface Canvas {
  background?: CanvasBackground;
  grid: CanvasGrid;
  breakpoints: Breakpoints;
}

// A concrete entity a slot resolves to — a device (by token) or an anchor target.
// This is the ENTITY only; the measurement names stay on the widget's SlotSelector
// (so a shared slot can feed different widgets different measurements). A slot's
// default binding lives in its SlotDefinition; the host's mount-time manifest can
// override it (see effectiveBindings).
export type SlotBinding =
  | { kind: 'device'; deviceToken: string }
  | { kind: 'anchor'; anchor: AnchorTarget };

// A named entity role a dashboard declares and its widgets reference via a
// SlotSelector. The host's binding manifest maps each slot to a concrete device or
// anchor at mount; `defaultBinding` is the slot's own binding (set by the authoring
// tenant) used when the host supplies no override — so a dashboard renders
// immediately for its author AND is export-ready as a template (strip the defaults,
// the host rebinds).
export interface SlotDefinition {
  type: 'device' | 'anchor';
  // Human-readable name shown in the binding UI, e.g. 'Primary thermostat'.
  label?: string;
  // The slot's default entity binding (the author's tenant); a host manifest overrides.
  defaultBinding?: SlotBinding;
}

export interface DashboardDefinition {
  schemaVersion: number;
  title: string;
  canvas: Canvas;
  widgets: WidgetInstance[];
  // Named entity roles bound at mount (ADR-039 runtime binding). Optional — absent
  // on a dashboard that uses no slots.
  slots?: Record<string, SlotDefinition>;
}

// ---- Live telemetry ---------------------------------------------------------

// A live measurement sample delivered to a widget. Mirrors event-management's
// MeasurementEvent; `deviceToken` is the device token the event carries (the
// value measurementStream is keyed on, per ADR-044).
export interface MeasurementSample {
  id: string;
  deviceToken: string;
  eventType: number;
  occurredTime: string | null;
  name: string;
  value: number | null;
  classifier: string | null;
}

// A raised alarm as an alarm widget sees it (ADR-041). Mirrors device-management's
// stored Alarm row (the source of truth), NOT the transient AlarmEvent envelope — the
// hub's alarm channel treats the live stream as a reconcile trigger and re-reads the
// authoritative rows via the alarms query. `token` is the stable alarm token (the row
// key + the ack/clear handle); `originatorToken` is the device token when the
// originator is a device (null otherwise). Kept decoupled from the device-management
// GraphQL types so the widget layer carries no service coupling.
export interface AlarmRow {
  token: string;
  originatorType: string;
  originatorToken: string | null;
  alarmKey: string;
  metricKey: string;
  state: string; // ACTIVE | CLEARED
  acknowledged: boolean;
  severity: string; // CRITICAL | MAJOR | MINOR | WARNING | INDETERMINATE
  raisedTime: string | null;
  clearedTime: string | null;
  acknowledgedTime: string | null;
  acknowledgedBy: string | null;
  lastValue: number | null;
  message: string | null;
}
