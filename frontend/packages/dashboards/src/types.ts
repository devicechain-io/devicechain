// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboard-definition contract (ADR-039). These types are the canonical
// shape of the JSON document that dashboard-management stores opaquely — the
// backend only validates it is well-formed JSON, so this package owns the shape.
// Kept deliberately fluid pre-GA. See research/dashboard-phase1-design-2026-07.md.

// ---- Datasource selectors ---------------------------------------------------
//
// A selector is a tagged union (discriminated on `kind`). The Hub resolves
// `device` and `anchor`, and treats `unbound` as zero devices. A `slot` never reaches
// it: the renderer resolves each slot through the settled bindings first (to a device,
// an anchor, or `unbound`). The remaining kinds are reserved — present so definitions
// stay forward-compatible, but the Hub rejects them until implemented.

// ---- Location series selection (ADR-078 decision 9) -------------------------
//
// A selector's `measurements: string[]` names NAMED SCALAR SERIES, and a position is
// not one of those: it is an atomic triple on its own event class and its own
// hypertable, deliberately never a metric. Naming a location through `measurements`
// would therefore be a lie the whole way down — the hub would resolve it against the
// measurement stream, which carries no coordinates at all.
//
// So a selector names a location series in its own field. The vocabulary is CLOSED
// rather than free text: a device has exactly one position track, so there is nothing
// to disambiguate by name, and an open string would invite an author to type a
// measurement name here and get silence.
//
//   'latest' — the last-known position, read from device-state's O(1) projection.
//
// A second member ('track', the position history from event-management) is the shape
// this exists to leave room for; the map widget reads 'latest' only.
export type LocationSeries = 'latest';

// The location series a selector names. An OBJECT rather than a bare string so a
// future member can carry its own parameters (a track's window) without changing
// every selector again.
export interface LocationSelection {
  series: LocationSeries;
}

// device — one device's measurements (latest-card, gauge, single-series chart), and/or
// its position when the selector names a location series.
export interface DeviceSelector {
  kind: 'device';
  deviceToken: string;
  measurements: string[];
  // Additive and OPTIONAL, deliberately: every dashboard definition stored before the
  // location channel existed carries no `location`, and must keep parsing and
  // rendering exactly as it did. Absence means "this selector names no location
  // series", which is the correct reading of every one of them.
  location?: LocationSelection;
}

// The dimension a dashboard aggregates over — a tracked relationship to a
// customer / area / asset. An anchor is not an opaque alias: it is a graph edge
// the platform already resolves events against.
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
  location?: LocationSelection;
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
  location?: LocationSelection;
}

export interface RelatedTraversalSelector {
  kind: 'relatedTraversal';
  from: string;
  relationship: string;
  measurements: string[];
  location?: LocationSelection;
}

// slot — a NAMED reference into the dashboard's `slots` section, resolved to a
// concrete entity at MOUNT by the host's binding manifest (ADR-039 runtime
// binding). This makes a definition a reusable TEMPLATE: the widget names the
// entity role (`slot`) and the measurements it wants, and two mounts of the same
// definition can bind the slot to two different devices. The RENDERER resolves it
// through the settled bindings (a slot's defaultBinding, overridable by the host and
// by a selection) before the data source sees it, so re-pointing a slot re-subscribes
// only the widgets bound to it; an unbound slot becomes an `unbound` selector and
// renders as an empty placeholder.
export interface SlotSelector {
  kind: 'slot';
  slot: string;
  measurements: string[];
  location?: LocationSelection;
}

// unbound — a slot that has NO binding, as the renderer hands it to a data source.
//
// It is never authored: the renderer produces it when it resolves a widget's `slot`
// selector through the settled bindings and finds nothing there (see
// resolveWidgetDatasource). Every live channel resolves it to ZERO devices — no stream,
// an empty alarm snapshot, no command target, no positions, and no availability check.
//
// 🔴 It is the OPPOSITE of an absent datasource, and the difference is not cosmetic. A
// widget with no datasource at all is tenant-wide on the alarm channel, so an unbound slot
// that collapsed into `undefined` would show every alarm in the tenant under a subtitle
// that implies one machine. Unbound is therefore a resolved state of its own, never a
// missing one.
//
// It carries the widget's series (measurement names, and the location series when one is
// named) so an offline preview source, which never resolves a device, can still fabricate
// the values a widget asks for. The live hub reads none of it.
export interface UnboundSelector {
  kind: 'unbound';
  measurements: string[];
  location?: LocationSelection;
}

export type DatasourceSelector =
  | DeviceSelector
  | AnchorSelector
  | DevicesSelector
  | RelatedTraversalSelector
  | SlotSelector
  | UnboundSelector;

// ---- Canvas + widgets -------------------------------------------------------

// The built-in widget types. Kept as a runtime array so parse/validation and the
// widget registry share one source of truth (WidgetType is derived from it).
//
// Widgets fall into data CHANNELS (see WIDGET_CHANNEL in @devicechain/widgets): the
// measurement widgets stream telemetry samples; the alarm widgets consume the raised-
// alarm surface (ADR-041) via the hub's alarm channel; the control widget (command-
// button) issues commands and tracks their delivery lifecycle via the command channel;
// the map widget reads device positions via the location channel (ADR-078 decision 9).
// A widget's channel decides which hook/registry the renderer binds it through.
export const WIDGET_TYPES = [
  'timeseries-chart',
  'latest-card',
  'gauge',
  'table',
  'label',
  'image',
  'alarm-table',
  'alarm-count',
  'alarm-severity',
  'command-button',
  'entity-selector',
  'map',
  'linear-gauge',
  'level-gauge',
  'battery',
  'status-light',
  'signal-bars',
  'compass',
  'progress-ring',
  'command-slider',
  'command-toggle',
  'stat-card',
  'bar-chart',
  'pie-chart',
  'fleet-table',
  'fleet-count',
  'device-status',
  'online-doughnut',
  'history-table',
] as const;

export type WidgetType = (typeof WIDGET_TYPES)[number];

// ---- Placement --------------------------------------------------------------
//
// Every widget is placed by an explicit MODE, modelled on CSS `position` and CSS anchor
// positioning, so whether a widget may overlap another is something it DECLARES rather than
// something a check has to catch afterwards:
//
//   grid      aligned to cells and occupying a footprint. Two grid footprints never overlap: the
//             parser refuses a definition where they do, so no producer (the editor, an import,
//             a Go builder, the API) can store one. The only mode that takes part in collision,
//             push-reflow and adaptive reflow.
//   relative  positioned against a HOST widget's live box, so it moves when the host moves or
//             reflows. It may sit over the host (a badge on a map) or beside it (a caption under
//             a chart). It never collides: overlapping is what the mode is for. It paints above
//             every grid widget.
//
// The union is OPEN: RESERVED_PLACEMENT_MODES names the modes a later release may add (`pinned`,
// fixed to a canvas edge; `sticky`, a grid widget that stays visible while the board scrolls).
// This release refuses them by name rather than mis-placing them as something else.
//
// There is no z-order and no pixel nudge: layering is the relative mode, and every distance is
// in cells, so an arrangement survives every screen size.

// A point on a box. For `anchor` it is a point on the HOST; for `self`, the point on the
// relative widget that is placed there.
export const PLACEMENT_ANCHORS = [
  'top-left',
  'top',
  'top-right',
  'left',
  'center',
  'right',
  'bottom-left',
  'bottom',
  'bottom-right',
] as const;
export type PlacementAnchor = (typeof PLACEMENT_ANCHORS)[number];

export const PLACEMENT_MODES = ['grid', 'relative'] as const;
export type PlacementMode = (typeof PLACEMENT_MODES)[number];
// Named so a definition from a release that implements them fails with a reason, not as a typo.
export const RESERVED_PLACEMENT_MODES = ['pinned', 'sticky'] as const;

// A footprint on the grid. `col`/`row` are the 0-based start cell; `cols`/`rows` how many cells
// it covers. Cells are square, so a span fixes the widget's SHAPE as well as its share.
export interface GridPlacement {
  mode: 'grid';
  col: number;
  row: number;
  cols: number;
  rows: number;
}

// A relative widget's own size: a span in cells (the grid's rule: n cells = n*cell +
// (n-1)*gutter), or exactly the host's box.
export type RelativeSize = { cols: number; rows: number } | { fill: 'host' };

// A widget placed against another. The geometry, in order: `anchor` picks a point on the host's
// box, `self` the point on this widget placed there, and `offset` translates it. An offset is a
// DISTANCE in cell PITCHES (one unit = live cell + gutter on that axis), so a whole-number offset
// lands exactly on a grid line, and fractions are allowed: `{x: -0.25, y: 0.25}` with
// anchor = self = 'top-right' insets a badge a quarter pitch from the host's corner at every
// cell size. The host's area plays no part.
export interface RelativePlacement {
  mode: 'relative';
  // The id of the widget this one is anchored to. It must exist; a relative host may itself be
  // relative only when ITS host is a grid widget (one level of chaining, no deeper).
  host: string;
  anchor: PlacementAnchor;
  // Absent = the same point as `anchor`.
  self?: PlacementAnchor;
  offset?: { x: number; y: number };
  size: RelativeSize;
  // Paint order among the relative widgets on the same host. Absent = 0.
  layer?: number;
}

export type Placement = GridPlacement | RelativePlacement;

// A widget's placement per breakpoint, keyed by breakpoint name. `base` is required; a widget
// with no entry for a breakpoint shows its `base` placement there.
//
// 🔑 A BREAKPOINT LAYOUT IS AN AUTHORED ARRANGEMENT AT ITS OWN COLUMN COUNT. Placements at a
// breakpoint are written in the column count canvas.breakpoints declares for it
// (breakpointColumns; `base` is always canvas.grid.columns), so a phone arrangement pinned at 4
// columns stays a 4-column arrangement. While the container is wide enough for the active
// breakpoint's column count at minCellSize, that arrangement renders as authored and takes
// precedence over the automatic reflow; only a container too narrow even for it reflows it.
// A board authored only at `base` simply reflows.
export type WidgetLayout = Record<string, Placement>;

// ---- Frames -----------------------------------------------------------------
//
// How a widget's card looks. `canvas.frame` sets the board's default and `widget.frame`
// overrides it field by field (resolveFrame). Every colour, radius and spacing is a TOKEN, never
// a free value: the parser refuses `#f00`, so dark mode, the ISA-101 theme and tenant branding
// stay coherent (a border of `sev-critical` is fine; a hex code is not).
//
// The border and padding sit INSIDE the widget's footprint, so changing them never moves the
// layout.

// The colour tokens a host defines, and the only colours a definition may name.
// @devicechain/widget-sdk's ThemeVar is this list.
export const THEME_COLOR_TOKENS = [
  'background',
  'foreground',
  'card',
  'card-foreground',
  'muted',
  'muted-foreground',
  'primary',
  'primary-foreground',
  'border',
  'destructive',
  'chart-1',
  'chart-2',
  'chart-3',
  'chart-4',
  'chart-5',
  'sev-critical',
  'sev-major',
  'sev-minor',
  'sev-warning',
] as const;
export type ThemeColorToken = (typeof THEME_COLOR_TOKENS)[number];

// The spacing steps, in px at density 1; every one multiplies by the density scale. `space-0`
// is zero, so a seamless tiled board is `gutter: 'space-0'` (or `gutter: 0`).
export const SPACE_TOKEN_PX = {
  'space-0': 0,
  'space-1': 4,
  'space-2': 8,
  'space-3': 12,
  'space-4': 16,
} as const;
export type SpaceTokenName = keyof typeof SPACE_TOKEN_PX;

export const FRAME_VARIANTS = ['card', 'flat', 'none'] as const;
export type FrameVariant = (typeof FRAME_VARIANTS)[number];
export const FRAME_RADII = ['none', 'sm', 'md', 'lg'] as const;
export type FrameRadius = (typeof FRAME_RADII)[number];
export const FRAME_BORDER_WIDTHS = [0, 1, 2, 3] as const;
export type FrameBorderWidth = (typeof FRAME_BORDER_WIDTHS)[number];
export const FRAME_HEADERS = ['auto', 'show', 'hide'] as const;
export type FrameHeader = (typeof FRAME_HEADERS)[number];

export interface FrameBorder {
  width?: FrameBorderWidth;
  radius?: FrameRadius;
  color?: ThemeColorToken;
}

// Every field optional: an absent field inherits (widget, then canvas, then the variant's preset).
//   variant  card = border + background + header (the default); flat = a card with no border;
//            none = frameless (no border, transparent, no header), the usual choice for a
//            relative badge or caption.
//   header   auto = the size-aware rules (the subtitle, then the title, give way as the widget
//            gets short); show = always; hide = never (the title stays the frame's aria-label).
export interface WidgetFrameSpec {
  variant?: FrameVariant;
  border?: FrameBorder;
  padding?: SpaceTokenName;
  background?: ThemeColorToken | 'transparent';
  header?: FrameHeader;
}

// A frame with every field decided: what the renderer hands the widget frame.
export interface ResolvedFrame {
  variant: FrameVariant;
  border: Required<FrameBorder>;
  padding: SpaceTokenName;
  background: ThemeColorToken | 'transparent';
  header: FrameHeader;
}

// The type a widget is given when its stored type is not one this build knows — one a
// newer release added. Deliberately NOT a member of WIDGET_TYPES: it is never authored,
// never offered in an add menu, and has no row in any per-type table. See
// UnknownWidgetInstance.
export const UNKNOWN_WIDGET_TYPE = 'unknown-widget';
export type UnknownWidgetType = typeof UNKNOWN_WIDGET_TYPE;

// The implementation a widget was authored against, stored beside its type as `widget`.
// `version` is provenance (the widget release the author used); `optionsVersion` and
// `sdkApiVersion` are the compatibility key together with the id — see checkWidgetCompatibility.
// A definition predating this field is a legacy built-in and carries none.
export interface WidgetImplementationRef {
  version: string;
  optionsVersion: number;
  sdkApiVersion?: number;
}

// A namespaced id (`scope/name`) a registered extension package owns. Kept apart from
// WidgetType so the built-in union stays exhaustive for the compiler.
export type ExtensionWidgetType = `${string}/${string}`;

// A widget of a type this build knows how to render.
export interface KnownWidgetInstance {
  id: string;
  type: WidgetType;
  // Absent on a legacy (unversioned) built-in.
  widget?: WidgetImplementationRef;
  layout: WidgetLayout;
  // label/image widgets carry no datasource.
  datasource?: DatasourceSelector;
  // Widget-specific options (series colors, unit, thresholds, …). Owned by the
  // widget package; opaque here.
  options?: Record<string, unknown>;
  // This widget's frame overrides, field by field over canvas.frame. Absent = the canvas's.
  frame?: WidgetFrameSpec;
  raw?: undefined;
  reason?: undefined;
}

// A widget from an installed extension package, recognized through the catalog the
// definition was parsed with: registered, versioned, and compatible (optionsVersion and
// SDK generation). Same envelope as a built-in; the id is a namespaced one.
export interface ExtensionWidgetInstance {
  id: string;
  type: ExtensionWidgetType;
  widget: WidgetImplementationRef;
  layout: WidgetLayout;
  datasource?: DatasourceSelector;
  options?: Record<string, unknown>;
  frame?: WidgetFrameSpec;
  raw?: undefined;
  reason?: undefined;
}

// Why a stored widget could not be resolved to an installed implementation.
//   unregistered     - no registration for the id in the catalog (or the type is unknown)
//   unversioned      - a registered extension stored without version metadata
//   invalid-metadata - `widget` is present but malformed, or an extension widget uses a
//                      top-level field name the format reserves (`raw`, `reason`)
//   options-version  - the stored settings shape differs from the installed one
//   sdk-incompatible - authored against a different SDK contract generation
export type UnresolvedWidgetCode =
  | 'unregistered'
  | 'unversioned'
  | 'invalid-metadata'
  | 'options-version'
  | 'sdk-incompatible';

export interface UnresolvedWidgetReason {
  code: UnresolvedWidgetCode;
  // A human-readable, specific explanation (names the id and the versions involved).
  message: string;
}

// A widget that cannot be resolved to an installed implementation. The parser keeps the
// stored object VERBATIM in `raw`, and the serializer writes `raw` back, so a host on an
// older release or without the package that loads a board and saves it does not delete or
// rewrite the widget it cannot read.
//
// `id` and `layout` are the only parsed fields, because every widget has them and the
// canvas needs them to place, select, move and delete the placeholder. An edit to
// either is written back into `raw` on save; nothing else about the widget is ever
// read or changed. It carries no datasource and no options: whatever the widget
// binds lives in `raw`, where no hub channel can mistake it for a selector this build
// understands. `reason` says why it is unresolved (derived at parse time, never stored).
export interface UnknownWidgetInstance {
  id: string;
  type: UnknownWidgetType;
  layout: WidgetLayout;
  raw: Record<string, unknown>;
  reason?: UnresolvedWidgetReason;
  widget?: undefined;
  datasource?: undefined;
  options?: undefined;
  frame?: undefined;
}

export type WidgetInstance = KnownWidgetInstance | ExtensionWidgetInstance | UnknownWidgetInstance;

// A widget some installed implementation resolved: a built-in or a registered extension, i.e.
// everything but a placeholder. What an editor panel is typed against.
export type ResolvedWidgetInstance = KnownWidgetInstance | ExtensionWidgetInstance;

export interface CanvasBackground {
  color?: string | null;
  imageUrl?: string | null;
}

// A length on the canvas: px at density 1 (multiplied by the density scale, like every size
// token), or a spacing token.
export type GridLength = number | SpaceTokenName;

// The canvas grid (the Android home-screen model): square cells of roughly constant size.
//
//   columns      the AUTHORED column count every `base` placement is written in.
//   minCellSize  px at density 1. While (container width - padding - (columns-1)*gutter.x) /
//                columns is at least this, the authored layout renders as authored and the cells
//                STRETCH (a wall gets bigger cells, hence bigger text). Narrower than that, the
//                board reflows onto fewer live columns. The gutters come out of the cell, never
//                the other way round, so widening them can never push a cell below this size.
//   gutter       the space between cells: one length, or one per axis.
//   padding      the margin around the board: one length, or one per side.
//
// Cells are square: a row is as tall as the live cell is wide.
export interface CanvasGrid {
  columns: number;
  minCellSize: number;
  gutter: GridLength | { x: GridLength; y: GridLength };
  padding: GridLength | { top: GridLength; right: GridLength; bottom: GridLength; left: GridLength };
}

// How the grid is sized into its container — a MOUNT/embed knob the host may
// override (renderer `sizing` prop). `fill`: the grid fills the container's width
// (the default; the "fit into an area" case). `{width}`: a fixed-px-wide container
// the fluid grid adjusts within. `{height}`: a fixed-px-tall container (rows scroll).
export type CanvasSizing = 'fill' | { width: number } | { height: number };

// A breakpoint: the container width it starts at, and optionally the column count its layout is
// authored in. A bare number is the width alone, and its layout is in canvas.grid.columns.
// `base` starts at 0 and is always in canvas.grid.columns.
export type BreakpointSpec = number | { minWidth: number; columns: number };

// Breakpoint name → spec. 'base' is required.
export type Breakpoints = Record<string, BreakpointSpec>;

// How roomy the board renders: a scale applied to every size token (font, spacing) and to the
// floor of the value-font curve. `comfortable` is the default and is not written when it is
// the choice; a wall display picks `wall`. A viewer can override it (the /dash app's
// `?density=`), so this is the AUTHORED default, not a fixed property of the board.
export const CANVAS_DENSITIES = ['compact', 'comfortable', 'wall'] as const;
export type CanvasDensity = (typeof CANVAS_DENSITIES)[number];
export const DEFAULT_CANVAS_DENSITY: CanvasDensity = 'comfortable';
export const DENSITY_SCALE: Readonly<Record<CanvasDensity, number>> = {
  compact: 0.9,
  comfortable: 1,
  wall: 1.5,
};

export function isCanvasDensity(value: unknown): value is CanvasDensity {
  return typeof value === 'string' && (CANVAS_DENSITIES as readonly string[]).includes(value);
}

export interface Canvas {
  background?: CanvasBackground;
  // Absent = `comfortable`.
  density?: CanvasDensity;
  grid: CanvasGrid;
  sizing: CanvasSizing;
  breakpoints: Breakpoints;
  // The board's default frame. Absent = a card.
  frame?: WidgetFrameSpec;
}

// A concrete entity a slot resolves to — a device (by token) or an anchor target.
// This is the ENTITY only; the measurement names stay on the widget's SlotSelector
// (so a shared slot can feed different widgets different measurements). A slot's
// default binding lives in its SlotDefinition; the host's mount-time manifest can
// override it (see effectiveBindings).
export type SlotBinding =
  | { kind: 'device'; deviceToken: string }
  | { kind: 'anchor'; anchor: AnchorTarget };

// A slot's dependency on a PARENT slot — the scoped-slot context hierarchy (ADR-039
// selection amendment). A scoped slot resolves RELATIVE to its parent's current
// binding: `parent` names an anchor-typed slot whose member devices are this slot's
// candidate set, and `strategy` picks among them:
//   'first'  — auto-bind the parent's first member (ordered by token); zero members ⇒
//              unbound. The slot follows the parent with no user input.
//   'manual' — keep the current pick iff it is still a member of the (possibly changed)
//              parent, else reset to unbound; a picker offers only the parent's members.
// A slot with no `scope` is a ROOT context (the top-level entity, e.g. a building). The
// relationship used to enumerate members is the parent anchor's own `relationship` (no
// separate field). Single parent ⇒ the slots form a forest (no diamonds), so the cascade
// order is well-defined. Replaces the reserved `entityFromState` selector.
export interface SlotScope {
  parent: string;
  strategy: 'first' | 'manual';
}

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
  // For a SCOPED slot this is a FALLBACK only — the cascade (resolveContextBindings)
  // derives the effective binding from the parent and always supersedes it (a `first`
  // slot ignores it; a `manual` slot uses it as the initial pick, kept iff a member).
  defaultBinding?: SlotBinding;
  // Optional dependency on a parent slot (the context hierarchy). Absent = a root context.
  scope?: SlotScope;
  // A ROOT anchor slot's picker may list only the areas of one area type, or the assets of one
  // asset type (a Floor picker that does not offer buildings and plants). Absent = every one the
  // viewer may see. Ignored for a customer anchor, a device slot and a scoped slot.
  candidates?: SlotCandidates;
}

// The type filter a root slot's picker applies, by the type's token: the area type or asset type
// of an anchor slot, the device type of a device slot.
export interface SlotCandidates {
  areaType?: string;
  assetType?: string;
  deviceType?: string;
}


// The entity kinds a root context-selector can list (the anchor target types + a bare
// device). A scoped child selector does NOT use this — its candidates are the parent
// anchor's member devices, enumerated via the DeviceResolver, not a list query.
export type EntityListKind = 'device' | 'customer' | 'area' | 'asset';

// EntityCandidateLister lists the entities of one kind (token + optional name) the viewer may see,
// backing a ROOT context-selector's candidate set. Injected by the host (createEntityLister)
// so the widgets/dashboards packages carry no extra device-management coupling beyond the
// one place (resolver.ts) that already owns it. A flat list — nested customer→area→asset
// tree picking is deferred.
export type EntityCandidateLister = (
  kind: EntityListKind,
) => Promise<Array<{ token: string; name?: string | null }>>;

// One option a context/entity-selector widget offers: the binding it would set, a display
// label, and whether it is the slot's current pick. Produced by resolveSlotCandidates.
export interface SelectionCandidate {
  binding: SlotBinding;
  label: string;
  selected: boolean;
}

// SelectionTarget is one view-driven selection: bind slot `slot` to `binding` (a device
// drill from an alarm originator, a context-selector pick). The host accumulates these
// into a selection overlay that the cascade resolves on top of the slot defaults. Named
// its TARGET SLOT (no channels — the slot IS the channel; the cascade fans a parent
// change out to its scoped children).
export interface SelectionTarget {
  slot: string;
  binding: SlotBinding;
}

// The definition format this release writes. 2 is the square-cell grid with explicit placement
// modes; 3 is the time span (a time-series widget draws a span, `windowSeconds`, not a sample
// count, and the board carries a `timeRange`). A stored document below it is converted on load
// (see ./legacy-grid and ./legacy-window).
export const CURRENT_SCHEMA_VERSION = 3;

// The board's time range as authored: the span (seconds) its time-series widgets draw until a
// viewer picks another. Absent = one hour (DEFAULT_TIME_RANGE_SECONDS).
export interface BoardTimeRange {
  defaultSeconds: number;
}

export interface DashboardDefinition {
  schemaVersion: number;
  title: string;
  canvas: Canvas;
  widgets: WidgetInstance[];
  // Named entity roles bound at mount (ADR-039 runtime binding). Optional — absent
  // on a dashboard that uses no slots.
  slots?: Record<string, SlotDefinition>;
  // The board's time range (D-1). Optional: a board that names none draws the default hour.
  timeRange?: BoardTimeRange;
  // Set by the parser, NEVER stored: the schemaVersion of the document this definition was
  // converted from, when it was stored in an older format and its layout was mapped onto the
  // current grid approximately. An editor shows a "review the layout" notice while it is set;
  // the serializer drops it, so the next save writes the current format and the notice goes.
  convertedFrom?: number;
  // Set by the parser, NEVER stored: the ids of the time-series widgets that carried a
  // sample-count window when the board was stored. A sample count has no span without a sampling
  // rate nobody recorded, so each of these now follows the board's time range instead; an editor
  // names them so the author can pin a `windowSeconds` where the old window mattered.
  convertedWindows?: string[];
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
  // Set only on a point that is a bucket AVERAGE rather than a reading: the bucket's width in seconds.
  // Stored history is drawn (and exported) per bucket, and a point read live is the reading itself;
  // this is how a consumer tells them apart. Absent on a reading.
  bucketSeconds?: number;
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
  // The originating device's type, present only on a row read for a subscription that asked for it
  // (AlarmSubscription.enrichDeviceType / deviceType). `undefined` means "not asked for or not
  // resolvable"; `null` means the originator is not a device. Additive: every other source omits it.
  deviceType?: AlarmDeviceType | null;
}

export interface AlarmDeviceType {
  token: string;
  name: string | null;
}

// One device's connectivity, as device-state holds it: whether it is considered connected, and when
// it last did anything. `lastActivityTime` is null for a device that has not produced an event.
// A device with no state row at all has no DevicePresence (the service omits it).
export interface DevicePresence {
  deviceToken: string;
  active: boolean;
  lastActivityTime: string | null;
  // Seconds without activity after which an INFERRED device is marked inactive.
  inactivityTimeout: number;
}

// How many matching alarms are at one severity (a severity-count read).
export interface AlarmSeverityCount {
  severity: string;
  count: number;
}

// How many matching alarms are on devices of one type at one severity (a type-count read).
export interface AlarmTypeCount {
  deviceType: AlarmDeviceType;
  severity: string;
  count: number;
}

// ---- Locations (location channel) -------------------------------------------

// A device's position as a map widget sees it. Mirrors device-state's LatestLocation
// projection row (the O(1) "where is it now" answer, not a scan of the history), kept
// decoupled from the device-state GraphQL types so the widget layer carries no service
// coupling — the same rule the alarm and command rows follow.
//
// 🔴 EVERY OPTIONAL IS NULLABLE AND ABSENT IS NOT ZERO. A device reports what its
// receiver knows: a fix with no heading has not reported due north, one with no speed
// has not reported stationary, one with no elevation has not reported sea level.
// Rendering any of those as 0 would invent a fact the platform deliberately declined
// to store, and the operator could not tell the invented value from a real one. Every
// consumer tests `!= null` (which admits a genuine 0 and rejects an absence) and shows
// NOTHING for an absent field.
//
// latitude/longitude are typed nullable because the projection's columns are — a row
// is never written without them, but the schema does not promise it, and a consumer
// that assumed non-null would place a marker at (0, 0), the Gulf of Guinea, rather
// than declining to place one at all.
//
// Units are fixed platform-wide and never per-device: WGS84 decimal degrees;
// elevation and accuracy in metres; speed in metres per second; heading in degrees
// clockwise from true north, [0, 360).
export interface LocationSample {
  id: string;
  deviceToken: string;
  latitude: number | null;
  longitude: number | null;
  elevation: number | null;
  accuracy: number | null;
  speed: number | null;
  heading: number | null;
  occurredTime: string | null;
}

// ---- Commands (control channel) ---------------------------------------------

// A device command as the command-button widget sees it (command-delivery). Mirrors
// the stored Command row: `token` is the client-minted dispatch id (also the cancel
// handle); `status` carries the lifecycle state as a plain string — the service
// declares no GraphQL enum, which is also why an unrecognized value has to stay
// survivable here. Kept decoupled from command-delivery's GraphQL types so the widget
// layer carries no service coupling.
//
// The lifecycle, in full (see ./command-status for the machine-readable copy):
//
//   non-terminal  QUEUED  accepted, awaiting its first dispatch decision
//                 HELD    dispatch deliberately withheld — the device is known absent,
//                         so the command waits rather than going into the void; this is
//                         where an offline fleet's backlog sits, possibly for days
//                 SENT    dispatched toward a reachable device, awaiting its response
//                 PARKED  published, but the device was not reachable, so it went
//                         nowhere; the platform still holds it and delivers it when the
//                         device wakes
//   terminal      SUCCESSFUL / FAILED   the device answered
//                 TIMEOUT               dispatched, never answered
//                 EXPIRED               TTL elapsed before it ever went out
//                 CANCELLED             an operator or tenant called it off
//
// EXPIRED and CANCELLED are separate ACTORS reaching the same dead end, not two names
// for one thing. Cancellation used to write EXPIRED and old rows were never backfilled,
// so both values legitimately appear in live data.
//
// Non-terminal is NOT the same as cancellable — SENT is the first and not the second (see
// ./command-status). A surface offering a cancel control must ask
// isCancellableCommandStatus rather than negating isTerminalCommandStatus.
export interface CommandRow {
  token: string;
  name: string;
  // QUEUED | HELD | SENT | PARKED | SUCCESSFUL | FAILED | TIMEOUT | EXPIRED | CANCELLED
  status: string;
  payload: string | null; // request JSON (as issued)
  responsePayload: string | null; // device response JSON
  error: string | null;
  queuedTime: string | null;
  sentTime: string | null;
  respondedTime: string | null;
}

// The scalar value types a command parameter can carry, reusing device-management's
// MetricDataType vocabulary (ADR-016).
export type CommandParamDataType = 'DOUBLE' | 'INT' | 'BOOLEAN' | 'STRING';

// One descriptor in a CommandDefinition's parameter schema (ADR-043). The console
// bakes the selected command's parsed schema into the widget's options at author time;
// the command-button widget renders it as a typed form. A SCALAR parameter is a single
// typed value (dataType + optional unit/bounds/enum/default); an OBJECT parameter nests
// a child `parameters` list. `kind` absent means SCALAR. Deliberately structural (no
// device-management import) so the widget package stays decoupled.
export interface CommandParameter {
  name: string;
  description?: string;
  kind?: 'SCALAR' | 'OBJECT';
  dataType?: CommandParamDataType;
  unit?: string;
  required?: boolean;
  default?: string | null;
  minValue?: number | null;
  maxValue?: number | null;
  enum?: string[];
  parameters?: CommandParameter[];
}
