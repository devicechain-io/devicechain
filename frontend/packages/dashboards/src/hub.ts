// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardHub — the dashboard runtime (ADR-039).
//
// It multiplexes every widget's live telemetry over the SDK's single per-area
// graphql-ws connection: many widgets bound to the same device share ONE upstream
// measurementStream subscription (ref-counted), so a crowded dashboard opens one
// stream per distinct device token, not one per widget. A per-widget subscription
// model fans out badly on a crowded board. The Hub owns the subscription
// lifecycle; widgets just hand it a datasource selector and a sink.

import { windowCriteria, type EventWindow } from './event-window';
import { gql, isForbiddenError, isScopeUnsupportedError, subscribe, type Area, type SubscriptionSink } from '@devicechain/client';

import { ConnectionTracker, type HubConnectionState } from './connection';
import type { HistoryWindow } from './history';
import {
  ACKNOWLEDGE_ALARM,
  ALARM_STREAM,
  ALARM_DEVICE_TYPES,
  ALARM_SEVERITY_COUNTS,
  ALARM_TYPE_COUNTS,
  ALARMS_QUERY,
  CLEAR_ALARM,
  type AlarmSearchCriteriaInput,
  type AlarmStreamResult,
} from './internal/alarm-doc';
import {
  COMMANDS_QUERY,
  CREATE_COMMAND,
} from './internal/command-doc';
import { LATEST_LOCATIONS_QUERY } from './internal/location-doc';
import { DEVICE_PRESENCE_QUERY } from './internal/presence-doc';
import { createLimiter, MEMBER_SEED_CONCURRENCY, SEED_CANCELLED } from './members';
import type { MemberBucketCriteria, MemberRef, MemberRoster, MemberSeed, MemberSeedPlan, MemberSink, PresenceSink, SeedFailure } from './members';
import { BUCKETED_MEASUREMENTS, type MeasurementBucket } from './queries';
import {
  MEASUREMENT_STREAM,
  type MeasurementStreamResult,
  type MeasurementStreamVariables,
} from './internal/measurement-doc';
import type {
  AlarmDeviceType,
  AlarmRow,
  AlarmSeverityCount,
  AlarmTypeCount,
  AnchorTarget,
  CommandRow,
  DatasourceSelector,
  DevicePresence,
  LocationSample,
  MeasurementSample,
  SlotBinding,
} from './types';

const EVENT_AREA: Area = 'event-management';
const DEVICE_AREA: Area = 'device-management';
const COMMAND_AREA: Area = 'command-delivery';
const STATE_AREA: Area = 'device-state';

// randomToken mints a command dispatch token (its idempotency key + cancel handle).
// crypto.randomUUID is only defined in a secure context, so fall back to a random-hex
// token for a plain-HTTP on-prem host — matching the guarded pattern generateWidgetId
// uses rather than throwing at send time.
function randomToken(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === 'function') return c.randomUUID();
  return `cmd-${Math.random().toString(16).slice(2)}-${Math.random().toString(16).slice(2)}`;
}

// Alarm channel cadence. The live stream is a best-effort trigger, so the poll is
// the correctness backstop (an alarm cleared while the socket was down still
// converges within one poll); the debounce coalesces a burst of events into one
// re-query.
const ALARM_RECONCILE_DEBOUNCE_MS = 800;
const ALARM_POLL_MS = 30_000;
// devicesByToken takes at most 1000 tokens.
const ALARM_DEVICE_TYPES_CHUNK = 1000;

// Command channel cadence. command-delivery exposes NO subscription, so the control
// channel is poll-only — but a command's lifecycle (QUEUED→SENT→SUCCESSFUL)
// resolves in seconds, so it polls far faster than the alarm channel. An issued command
// reconciles immediately (not on the next tick) so the operator sees it appear at once.
const COMMAND_POLL_MS = 4_000;

// Location channel cadence. device-state exposes NO location subscription either, so
// like the control channel this is poll-only — but the two poll for opposite reasons,
// and the cadence follows from that rather than from copying a number:
//
//   • the control channel polls FAST (4s) for a SHORT time: a command's lifecycle is a
//     burst that reaches a terminal status in seconds and then stops changing.
//   • the alarm channel polls SLOWLY (30s) because it is only a backstop — the live
//     ALARM_STREAM does the real work and the poll exists for what the socket missed.
//
// A fleet's positions have neither property: they change continuously, for as long as
// the board is open, with nothing else watching them. So this poll is not a backstop,
// it IS the channel, and its cost is a standing one. What makes a middle cadence
// affordable is the BATCH query: one `latestLocations` round trip per tick covers
// every device on the widget, so the cost is per-poll, not per-marker, and a 200-device
// map costs what a 2-device map costs. Anything materially faster than this wants a
// subscription rather than a tighter poll — a tighter poll would multiply a whole
// board's queries against a projection that is only written when a device moves.
const LOCATION_POLL_MS = 15_000;

// Presence channel cadence. device-state exposes NO presence subscription, so like the location
// channel this is poll-only and the poll IS the channel; the same batch-per-tick reasoning applies
// (one `deviceStatesByDeviceToken` round trip per tick covers every device on the widget). Presence
// changes on the scale of an inactivity timeout (minutes), so it polls at the location cadence rather
// than faster: a tighter poll would multiply a board's queries to learn nothing sooner.
const PRESENCE_POLL_MS = 15_000;
// deviceStatesByDeviceToken refuses more than 1000 tokens with LIMIT_EXCEEDED.
const PRESENCE_CHUNK = 1000;

// DeviceResolver turns the graph references in a dashboard definition into the
// device tokens event-management keys on (measurementStream(deviceToken:), per
// ADR-044). It is injected so this package carries no device-management coupling
// and stays unit-testable; a host backs it with device-management queries.
export interface DeviceResolver {
  // The device tokens currently anchored to the given target. This is where
  // "the Hub expands an anchor to its current membership" lives (Phase 1);
  // server-side expansion is a Phase-2 optimization.
  devicesForAnchor(anchor: AnchorTarget): Promise<string[]>;
  // Whether a device with this token currently exists (device-management). Backs the
  // widget availability check: a dashboard references a device by a stable token
  // (ADR-044), and a since-deleted device's token no longer resolves.
  deviceExists(deviceToken: string): Promise<boolean>;
  // The anchor's members with their names, and the anchor's true size (which can exceed the page
  // the resolver reads). Optional: a resolver without it yields tokens only, and a fleet widget then
  // labels rows by token and cannot report a truncated anchor.
  anchorMembers?(anchor: AnchorTarget): Promise<{ members: MemberRef[]; total: number }>;
  // The measurement names the ACTIVE profile version of a device type declares (calculated metrics
  // included), or null when it declares nothing to consult. Rejects when the read fails. Optional:
  // a resolver without it leaves every member's declaration unknown.
  declaredMetrics?(deviceType: string): Promise<string[] | null>;
}

// WidgetStreamSink receives live samples for one widget, across every device its
// datasource resolves to. next() fires per sample; error() once if selector
// resolution or the socket fails. reconnected() fires when one of its device streams resumes
// after an interruption -- the socket dropped and came back, or the server ended the stream and it
// was opened again. The samples sent meanwhile were never delivered and never will be: a widget
// that draws history re-reads it rather than joining the readings either side of the outage.
// Optional (a source that never drops a feed does not call it).
export interface WidgetStreamSink {
  next: (sample: MeasurementSample) => void;
  error?: (err: unknown) => void;
  reconnected?: () => void;
}

// ── Alarm channel ────────────────────────────────────────────────────────
//
// Alarm widgets consume a different surface than telemetry: the raised-alarm rows
// (ADR-041), read query-then-reconcile. An alarm subscription describes the SCOPE
// (which entity's alarms) plus the server-side filters, and receives whole snapshots
// (not incremental events), because the authoritative rows come from the query.

// AlarmSubscription is one alarm widget's interest: its scope selector (undefined =
// tenant-wide — every alarm the viewer can see) plus filters. `pageSize` bounds the
// rows returned (an alarm table shows the newest N); the total count reported in a
// snapshot is independent of it (server totalRecords), so an alarm-count reflects the
// true match count even past the page.
export interface AlarmSubscription {
  datasource?: DatasourceSelector;
  state?: string;
  severity?: string;
  acknowledged?: boolean;
  pageSize: number;
  // Resolve each row's originating device type (AlarmRow.deviceType) with one batched read per
  // reconcile. Off by default: an alarm table that does not group by type pays nothing.
  enrichDeviceType?: boolean;
  // Keep only alarms whose originating device has this device-type TOKEN.
  // Filtered by device-management, so `total` is the exact match count.
  deviceType?: string;
  // Read the per-type counts of the matching alarms instead of the alarms: the snapshot then has no
  // rows, `typeCounts` is set, and `total` is their sum. A source that cannot count by type leaves
  // `typeCounts` unset (or fails the stream); it never answers with counts it did not make.
  countByDeviceType?: boolean;
  // Read the per-severity counts of the matching alarms instead of the alarms: the snapshot then has
  // no rows, `severityCounts` is set, and `total` is their sum. Tenant-wide, EVERY alarm the viewer
  // can see counts, whatever its originator (a device, an asset, an area, a customer, a group), so
  // the total is the alarm count's. With `deviceType`, only alarms on devices of that type count. A
  // source that cannot count by severity leaves `severityCounts` unset (or fails the stream).
  countBySeverity?: boolean;
  // Keep only alarms whose span (raise to clear, or still active) overlaps this range, filtered
  // by device-management, so `total` is the exact count in range. An alarm keeps only its latest
  // raise, so an earlier raise of the same alarm is in no range.
  window?: EventWindow;
}

// A full alarm snapshot: the current rows (newest first, capped to pageSize) and the
// total number of alarms matching the filter (past the page). One replaces the last.
export interface AlarmSnapshot {
  alarms: AlarmRow[];
  total: number;
  // Only for a subscription with countByDeviceType: one entry per (device type, severity) present.
  typeCounts?: AlarmTypeCount[];
  // Only for a subscription with countBySeverity: one entry per severity present.
  severityCounts?: AlarmSeverityCount[];
}


export interface AlarmStreamSink {
  next: (snapshot: AlarmSnapshot) => void;
  error?: (err: unknown) => void;
}

// ── Control channel ──────────────────────────────────────────────────────
//
// The command-button widget issues commands to a device and watches their delivery
// lifecycle. Unlike telemetry/alarms there is no live subscription (command-delivery
// exposes none), so the channel is poll-only: a device-scoped `commands` query re-read
// on a short interval (and immediately after an issue). A command targets ONE device,
// so a subscription resolves its scope to a single device token.

// CommandSubscription is one command widget's interest: its scope (which device) plus
// the page size bounding the recent-command history it shows. A command button binds a
// single device; an unscoped (or unresolved) widget has no target and renders empty.
export interface CommandSubscription {
  datasource?: DatasourceSelector;
  pageSize: number;
  // Keep only commands sent inside this range (queued, for one never sent), filtered by
  // command-delivery, so `total` is the exact count in range.
  window?: EventWindow;
}

// A command-history snapshot: the resolved target device (null when unbound — the
// widget then can't issue), the recent commands (newest first, capped to pageSize) and
// the total matching count. One replaces the last (poll-then-emit).
export interface CommandSnapshot {
  deviceToken: string | null;
  commands: CommandRow[];
  total: number;
}

export interface CommandStreamSink {
  next: (snapshot: CommandSnapshot) => void;
  error?: (err: unknown) => void;
}

// The result of issuing a command. It is a DISCRIMINATED UNION rather than a token,
// because the enqueue has two possible answers and a widget must not show them the same
// way:
//
//   - 'sent'     — the command was created; `token` is its freshly-minted dispatch
//                  token, so the widget can highlight the command it just issued as it
//                  moves through the lifecycle.
//   - 'rejected' — the server REFUSED the request and said why: `code` is the stable
//                  classification to branch on (DEVICE_NOT_FOUND,
//                  COMMAND_NOT_IN_VOCABULARY, PAYLOAD_SCHEMA_VIOLATION,
//                  PAYLOAD_NOT_JSON, METADATA_NOT_JSON, EXPIRES_AT_INVALID,
//                  HELD_CEILING_EXCEEDED, … — an OPEN set), `reason` is client-safe
//                  prose to show a person.
//
// A THROWN error is neither: it means the platform could not decide the enqueue at all
// (the service unreachable, a database error), which says nothing about the command and
// so deserves a generic failure message rather than a reason.
//
// The union is deliberately not "token, plus an optional rejection". A caller reading
// `.token` off a refusal would compile, highlight nothing, and report no failure — the
// send would look successful. The discriminant makes the refusal impossible to ignore
// at the type level.
export type CommandDispatch =
  | { status: 'sent'; token: string }
  | { status: 'rejected'; code: string; reason: string };

// ── Location channel ─────────────────────────────────────────────────────
//
// The map widget consumes a fifth surface: where the bound devices ARE. Like the
// control channel there is no live subscription to ride (device-state exposes none for
// position), so it is query-then-poll — the alarm and command channels are the
// precedent here, not the measurement channel.
//
// What is different from all three is the REFUSAL. Position is gated on its own
// `location:read` authority, which is deliberately absent from the read-only viewer
// baseline, so an ordinary member with full telemetry access is routinely refused. The
// channel therefore turns a refusal into a VALUE rather than an error: it is not a
// fault, and it is not emptiness either.

// LocationSubscription is one map widget's interest: the selector naming both the
// devices and the location series to read. There is no page size — a map shows every
// device it is bound to, and the bound set is the selector's own resolution.
export interface LocationSubscription {
  datasource?: DatasourceSelector;
}

// A location snapshot, discriminated so the refusal cannot be mistaken for emptiness.
//
// 🔴 `positions` with an empty `locations` and `forbidden` are OPPOSITE facts, and only
// one of them is actionable. "No device here has ever reported a position" is a claim
// about the DEVICES; "you may not view location" is a claim about the CALLER. Folding
// them together (a bare `locations: []`) would tell an operator their fleet is
// unlocated when the truth is that they need a role — which is the exact mistake the
// device-detail position panel was built to avoid, held here in the type.
//
// `deviceTokens` is the set the selector resolved to, carried alongside the positions
// because a never-located device is ABSENT from the query result: without it, "bound to
// nothing" and "bound to devices that have never moved" are indistinguishable.
export type LocationSnapshot =
  | { kind: 'positions'; deviceTokens: string[]; locations: LocationSample[] }
  | { kind: 'forbidden' };

export interface LocationStreamSink {
  next: (snapshot: LocationSnapshot) => void;
  error?: (err: unknown) => void;
}

// ── Action seam (writes) ─────────────────────────────────────────────────
//
// Read widgets are pure `(widget, data)`; a widget that ACTS (acknowledge/clear an
// alarm, send a command) never touches the SDK either — it calls this seam, which the
// renderer threads in from the runtime. So preview stays offline (SyntheticDataSource
// implements a no-op/echo WidgetActions) and the "widget never reaches the backend"
// invariant holds. `can` gates the UI: a widget hides an action the viewer isn't
// authorized for (the server enforces it regardless).
//
// Growing this interface is a breaking change for any external host implementing it.
// The alarm actions (acknowledgeAlarm/clearAlarm) are the required baseline; capabilities
// added later — like sendCommand below — are declared OPTIONAL so a host predating them
// still satisfies the type, and consumers feature-detect (typeof actions?.sendCommand).
export interface WidgetActions {
  // Acknowledge / clear a raised alarm by token (requires alarm:write). Resolves when
  // the mutation succeeds; the runtime reconciles the affected alarm widgets so the
  // change shows immediately rather than waiting for the next poll.
  acknowledgeAlarm(alarmToken: string): Promise<void>;
  clearAlarm(alarmToken: string): Promise<void>;
  // Issue a command to a device (requires command:write). The runtime mints the dispatch
  // token and returns it; it also reconciles the command widgets so the new command
  // appears at once. It RESOLVES on a refusal too — with a 'rejected' dispatch carrying
  // the server's code + reason (see CommandDispatch) — and rejects only when the enqueue
  // could not be decided at all. OPTIONAL: added after this interface shipped, so a host
  // predating it (or a strictly read-only one) may omit it — a command widget then
  // renders its Send control disabled. `payload` is the request body sent to the device
  // verbatim (the widget serializes its typed parameter form to JSON).
  sendCommand?(deviceToken: string, name: string, payload?: string): Promise<CommandDispatch>;
  // Whether the current viewer holds an authority (e.g. 'alarm:write'). Drives whether
  // an action control renders at all.
  can(authority: string): boolean;
}

// The hub holds NO slot bindings. A widget's `slot` selector is resolved by the renderer
// (resolveWidgetDatasource) through the settled bindings before it reaches the hub, so one
// hub serves a board for its whole life: a selection that re-points a slot changes the
// concrete selector of just the widgets bound to it, and only those re-subscribe. A host
// builds one hub per (resolver, authorities).
export interface DashboardHubConfig {
  resolver: DeviceResolver;
  // The current viewer's authorities (access-token claims). Drives `can()` — which
  // gates whether a widget's action controls render. Omitted/empty = no write actions
  // (the read-only default); '*' grants all. The server enforces authority regardless.
  authorities?: string[];
}

// WidgetDataSource is the minimal contract a widget renderer needs from a data
// source: bind a datasource selector to a sink, get a disposer back. DashboardHub
// is the live implementation (multiplexed backend telemetry); SyntheticDataSource
// is the offline preview implementation. The widget layer depends on THIS interface,
// not the concrete class, so a host can feed either without widgets knowing which.
export interface WidgetDataSource {
  subscribeWidget(datasource: DatasourceSelector, sink: WidgetStreamSink): () => void;
  // Bind an alarm widget's scope+filters to a sink; returns a disposer. Delivers
  // whole snapshots (query-then-reconcile), not incremental events. Implemented by
  // both the live hub and the synthetic preview source so alarm widgets render
  // identically from either.
  subscribeAlarms(subscription: AlarmSubscription, sink: AlarmStreamSink): () => void;
  // Bind a command widget's scope to a sink; returns a disposer. Delivers whole
  // command-history snapshots on a poll (command-delivery has no subscription).
  // Implemented by both the live hub and the synthetic preview source.
  subscribeCommands(subscription: CommandSubscription, sink: CommandStreamSink): () => void;
  // Bind a map widget's selector to a sink; returns a disposer. Delivers whole
  // position snapshots on a poll (device-state has no location subscription), and
  // reports a `location:read` refusal as a snapshot state rather than an error.
  // Implemented by both the live hub and the synthetic preview source.
  subscribeLocations(subscription: LocationSubscription, sink: LocationStreamSink): () => void;
  // Whether a widget's bound entity still exists. Optimistic + async: a widget renders
  // from its stream immediately, and this resolves separately — only a device selector
  // (or a slot bound to a device) whose token no longer resolves reports false, so the
  // widget shows "unavailable" instead of a blank pane. Anchor / unbound-slot / no-
  // datasource report true (their empty state is legitimate, not "unavailable"). Fails
  // OPEN (true) on an inconclusive check, so a device-management blip never falsely marks
  // a live device unavailable.
  isDatasourceAvailable(datasource: DatasourceSelector | undefined): Promise<boolean>;
  // Whether the platform offers this binding to the viewer at all. False only when the server
  // refuses it as not available to a member limited to part of the tenant (a customer user):
  // an area or asset anchor, whose members are found through the relationship graph such a
  // member cannot read. The widget then says so rather than rendering empty or "no access",
  // since nothing that member could be granted would change it. Optional: a source without it
  // (the synthetic preview, a recorded run) supports every binding. Fails open (true) on any
  // other failure, which the widget's own channel reports.
  isDatasourceSupported?(datasource: DatasourceSelector | undefined): Promise<boolean>;
  // The source's idea of "now" (epoch ms), for measuring a time span against its samples. Omitted
  // = the wall clock, which is right for every live source. A source that plays back a recorded
  // run implements it, because the run's samples are dated by the run.
  now?(): number;
  // The time buckets of a selector's measurements over a window, each carrying the exact count,
  // minimum, maximum and sum of its readings: what a stat widget folds into the span's aggregates.
  // Optional: a source that cannot produce them omits it, and the widget then says so instead of
  // showing a number. Rejects when the read fails.
  bucketMeasurements?(datasource: DatasourceSelector, window: HistoryWindow): Promise<MeasurementBucket[]>;
  // A fleet widget's roster and live feed: the members the selector resolves to (delivered once),
  // then every member's samples, each tagged with its device. ONE subscription per widget, however
  // many members; the hub shares each member's upstream stream with every other widget on it.
  // Optional: a source without it leaves a fleet widget with an explained "not supported" state.
  subscribeMembers?(datasource: DatasourceSelector, sink: MemberSink): () => void;
  // Each device's seed, made by the widget's plan from the reads it asks for, delivered per device as
  // it settles (`null` when that device's plan failed; `failure` says why). Every read is bounded
  // hub-wide (MEMBER_SEED_CONCURRENCY) and shared with any identical one in flight. `cancelled` is
  // checked before each read starts. Optional like the above.
  readMemberSeeds?(
    deviceTokens: readonly string[],
    plan: MemberSeedPlan,
    onSeed: (deviceToken: string, seed: MemberSeed | null, failure?: SeedFailure) => void,
    cancelled?: () => boolean,
  ): Promise<void>;
  // The measurement names a device TYPE's active profile version declares: what separates "does not
  // report this" (the profile never declares it) from "not reporting" (it does, and the device is
  // silent). `null` when it cannot say -- no published profile, no way to look, or the lookup
  // failed -- and an unknown declaration is read as declared, never as absent. Optional like the above.
  declaredMetrics?(deviceType: string): Promise<readonly string[] | null>;
  // A presence widget's roster and connectivity: the members the selector resolves to (delivered
  // once), then a whole snapshot of their device-state on every poll (online or offline, last active).
  // Optional: a source with no device-state to read (a recorded run) omits it, and the widget then
  // says so rather than draw a status it cannot know.
  subscribePresence?(datasource: DatasourceSelector, sink: PresenceSink): () => void;
}

// One widget's interest in a device stream: the measurement names it wants (an
// empty set means every measurement) and where to deliver them.
interface Subscriber {
  names: Set<string>;
  sink: WidgetStreamSink;
}

// The shared upstream for one distinct device token.
interface DeviceStream {
  subscribers: Set<Subscriber>;
  unsubscribe: () => void;
}

export class DashboardHub implements WidgetDataSource, WidgetActions {
  private readonly resolver: DeviceResolver;
  // The viewer's authorities (for can()); '*' is the superuser wildcard.
  private readonly authorities: ReadonlySet<string>;
  // Per alarm-subscription reconcile triggers — invoked after an ack/clear so the
  // affected alarm widgets refresh immediately instead of waiting for the poll/stream.
  private readonly alarmReconcilers = new Set<() => void>();
  // Per command-subscription reconcile triggers — invoked after an issue so the command
  // widgets show the new command immediately instead of waiting for the next poll tick.
  private readonly commandReconcilers = new Set<() => void>();
  // One entry per distinct device token that has at least one subscriber.
  private readonly streams = new Map<string, DeviceStream>();
  // Live alarm-subscription disposers. The alarm channel isn't ref-counted through
  // `streams` (it holds a poll/debounce/trigger per subscription, not a shared device
  // stream), so its disposers are tracked here for disposeAll() to reach — otherwise an
  // imperative host closing the dashboard would leak every alarm widget's poll + socket.
  private readonly alarmDisposers = new Set<() => void>();
  // Live command-subscription disposers (same rationale as alarmDisposers — the control
  // channel holds a poll per subscription, not a shared device stream), so disposeAll()
  // can tear down every command widget's poll.
  private readonly commandDisposers = new Set<() => void>();
  // Live location-subscription disposers (same rationale as alarmDisposers/
  // commandDisposers — the location channel holds a poll per subscription, not a shared
  // device stream), so disposeAll() can tear down every map widget's poll.
  private readonly locationDisposers = new Set<() => void>();
  // Live presence-subscription disposers (same rationale: a poll per subscription).
  private readonly presenceDisposers = new Set<() => void>();

  // Which of this hub's sockets are down, folded into one state a header can show.
  private readonly connection = new ConnectionTracker();
  // Streams evicted by an upstream error that still have subscribers: out of `streams`, but
  // still holding their connection handle (see ensureStream), so disposeAll() can reach them.
  private readonly deadStreams = new Set<DeviceStream>();

  constructor(config: DashboardHubConfig) {
    this.resolver = config.resolver;
    this.authorities = new Set(config.authorities ?? []);
  }

  // The hub's live-connection state: `live`, `reconnecting` (a socket dropped and is being
  // retried) or `offline` (down past the point a retry is a blip, or the browser is offline).
  // A hub with nothing subscribed has nothing to lose and reads `live`. Pair with
  // onConnectionState (a subscribe/getSnapshot pair, so it drops into useSyncExternalStore).
  get connectionState(): HubConnectionState {
    return this.connection.state;
  }

  onConnectionState(listener: () => void): () => void {
    return this.connection.subscribe(listener);
  }

  // subscribeWidget binds a widget's datasource to a sink and returns a disposer.
  // Selector resolution is async (anchor→devices); the disposer is returned
  // synchronously and cancels a still-pending resolution, so tearing a widget down
  // before its streams open never attaches a leaked subscriber.
  subscribeWidget(datasource: DatasourceSelector, sink: WidgetStreamSink): () => void {
    let disposed = false;
    const detachers: Array<() => void> = [];
    const dispose = (): void => {
      disposed = true;
      for (const detach of detachers.splice(0)) detach();
    };

    this.resolveDevices(datasource)
      .then((groups) => {
        if (disposed) return;
        for (const group of groups) {
          detachers.push(this.attach(group.deviceToken, group.names, sink));
        }
      })
      .catch((err) => {
        if (!disposed) sink.error?.(err);
      });

    return dispose;
  }

  // subscribeMembers: the roster, then the live samples of every member. Resolution is async, so
  // the disposer is returned at once and cancels a still-pending resolution (a widget torn down
  // before its roster arrives never attaches a leaked subscriber). Each member rides the same
  // ref-counted device stream every other widget on it uses.
  subscribeMembers(datasource: DatasourceSelector, sink: MemberSink): () => void {
    let disposed = false;
    const detachers: Array<() => void> = [];
    this.resolveRoster(datasource)
      .then(({ roster, names }) => {
        if (disposed) return;
        sink.roster(roster);
        for (const member of roster.members) {
          detachers.push(
            this.attach(member.token, names, {
              next: (sample) => sink.next(sample),
              error: (err) => sink.error?.(err),
              reconnected: () => sink.reconnected?.(),
            }),
          );
        }
      })
      .catch((err) => {
        if (disposed) return;
        if (isForbiddenError(err)) sink.forbidden?.();
        else sink.error?.(err);
      });
    return () => {
      disposed = true;
      for (const detach of detachers.splice(0)) detach();
    };
  }

  // One resolution of a selector into a roster. An anchor goes through the resolver's member read
  // when it has one (names and the true size); everything else is the tokens resolveDevices yields.
  private async resolveRoster(datasource: DatasourceSelector): Promise<{ roster: MemberRoster; names: Set<string> }> {
    const names = new Set(datasource.measurements);
    if (datasource.kind === 'anchor' && this.resolver.anchorMembers) {
      const { members, total } = await this.resolver.anchorMembers(datasource.anchor);
      return { roster: { members, total, unbound: false }, names };
    }
    const groups = await this.resolveDevices(datasource);
    const members = groups.map((g) => ({ token: g.deviceToken, name: null }));
    return { roster: { members, total: members.length, unbound: datasource.kind === 'unbound' }, names };
  }

  // One read per distinct device type, whoever asks and however many devices have it. A settled
  // answer (names, or null for "no published profile") is kept for the session; a failed read is
  // not, so a later ask tries again, and that ask is answered null meanwhile.
  private readonly declared = new Map<string, Promise<readonly string[] | null>>();

  declaredMetrics(deviceType: string): Promise<readonly string[] | null> {
    const read = this.resolver.declaredMetrics;
    if (!read) return Promise.resolve(null);
    let pending = this.declared.get(deviceType);
    if (!pending) {
      pending = read.call(this.resolver, deviceType).catch(() => {
        this.declared.delete(deviceType);
        return null;
      });
      this.declared.set(deviceType, pending);
    }
    return pending;
  }

  // The hub-wide cap on in-flight member seed reads, and the reads currently in flight (so a second
  // widget asking the same question of the same device shares one request).
  private readonly seedLimiter = createLimiter(MEMBER_SEED_CONCURRENCY);
  private readonly seedReads = new Map<string, Promise<MeasurementBucket[]>>();

  async readMemberSeeds(
    deviceTokens: readonly string[],
    plan: MemberSeedPlan,
    onSeed: (deviceToken: string, seed: MemberSeed | null, failure?: SeedFailure) => void,
    cancelled: () => boolean = () => false,
  ): Promise<void> {
    await Promise.all(
      deviceTokens.map(async (token) => {
        let seed: MemberSeed;
        try {
          seed = await plan.seed(token, (criteria) => this.memberBuckets(token, criteria, cancelled));
        } catch (err) {
          if (cancelled() || err === SEED_CANCELLED) return;
          onSeed(token, null, isForbiddenError(err) ? 'forbidden' : 'failed');
          return;
        }
        if (!cancelled()) onSeed(token, seed);
      }),
    );
  }

  private async memberBuckets(token: string, criteria: MemberBucketCriteria, cancelled: () => boolean): Promise<MeasurementBucket[]> {
    const key = JSON.stringify([token, criteria]);
    for (;;) {
      let read = this.seedReads.get(key);
      if (!read) {
        // The first asker's `cancelled` decides whether the read starts.
        read = this.seedLimiter(
          () => gql(EVENT_AREA, BUCKETED_MEASUREMENTS, { criteria: { deviceToken: token, ...criteria } }).then((r) => r.bucketedMeasurements),
          cancelled,
        );
        this.seedReads.set(key, read);
        const forget = () => this.seedReads.delete(key);
        read.then(forget, forget);
      }
      try {
        return await read;
      } catch (err) {
        // A read this asker shared with one that has since gone away was dropped before it started;
        // this asker is still here, so it asks again (now as the first asker).
        if (err === SEED_CANCELLED && !cancelled()) continue;
        throw err;
      }
    }
  }

  // bucketMeasurements reads the buckets of every (device, measurement) pair over a window. Widgets
  // that ask the same question while a read is in flight share it: stat cards on one measurement
  // and span read on the same minute boundaries, so a board of them costs one read per question.
  private readonly bucketReads = new Map<string, Promise<MeasurementBucket[]>>();

  bucketMeasurements(datasource: DatasourceSelector, window: HistoryWindow): Promise<MeasurementBucket[]> {
    const key = JSON.stringify([datasource, window]);
    let read = this.bucketReads.get(key);
    if (!read) {
      read = this.readBuckets(datasource, window);
      this.bucketReads.set(key, read);
      const forget = () => this.bucketReads.delete(key);
      read.then(forget, forget);
    }
    return read;
  }

  private async readBuckets(datasource: DatasourceSelector, window: HistoryWindow): Promise<MeasurementBucket[]> {
    const groups = await this.resolveDevices(datasource);
    const reads = groups.flatMap((g) =>
      (g.names.size ? [...g.names] : [undefined]).map((name) =>
        gql(EVENT_AREA, BUCKETED_MEASUREMENTS, { criteria: { deviceToken: g.deviceToken, name, ...window } }),
      ),
    );
    return (await Promise.all(reads)).flatMap((r) => r.bucketedMeasurements);
  }

  // subscribeAlarms binds an alarm widget's scope+filters to a sink and returns a
  // disposer. Unlike the measurement channel it is NOT multiplexed — alarm widgets are
  // few, and each carries its own filter — so every subscription opens its own trigger
  // stream + reconcile poll (sharing one tenant-wide trigger stream across widgets is a
  // deferred optimization). Query-then-reconcile: an initial query, then the live
  // ALARM_STREAM debounced into re-queries, plus a poll backstop and a reconnect
  // re-query. Scope resolution is async (slot/anchor → devices); the disposer is
  // returned synchronously and cancels a still-pending resolution.
  subscribeAlarms(subscription: AlarmSubscription, sink: AlarmStreamSink): () => void {
    let disposed = false;
    let debounce: ReturnType<typeof setTimeout> | undefined;
    let poll: ReturnType<typeof setInterval> | undefined;
    let unsubscribe: (() => void) | undefined;
    let reconciler: (() => void) | undefined;
    // Monotonic generation: only the newest reconcile's result may be emitted, so a
    // slow query that resolves after a newer one can't overwrite fresher rows.
    let generation = 0;

    const dispose = (): void => {
      disposed = true;
      if (debounce) clearTimeout(debounce);
      if (poll) clearInterval(poll);
      unsubscribe?.();
      if (reconciler) this.alarmReconcilers.delete(reconciler);
      this.alarmDisposers.delete(dispose);
    };
    this.alarmDisposers.add(dispose);

    const reconcile = (tokens: string[], tenantWide: boolean): void => {
      const gen = ++generation;
      this.queryAlarms(subscription, tokens, tenantWide)
        .then((snapshot) => {
          if (!disposed && gen === generation) sink.next(snapshot);
        })
        .catch((err) => {
          if (!disposed && gen === generation) sink.error?.(err);
        });
    };

    this.resolveAlarmScope(subscription.datasource)
      .then((scope) => {
        if (disposed) return;

        // A scoped widget that resolves to no device (an unbound slot, an empty anchor)
        // shows an empty state — NOT tenant-wide. Only a widget with no datasource at
        // all is tenant-wide. Nothing to stream/poll here. Scope is resolved once (like
        // the measurement channel): a slot rebind arrives as a new concrete selector, which
        // re-subscribes this widget, but organic anchor-membership change isn't picked up
        // until the widget re-subscribes — a deferred enhancement shared with the
        // measurement channel.
        if (!scope.tenantWide && scope.tokens.length === 0) {
          sink.next({ alarms: [], total: 0 });
          return;
        }

        const trigger = (): void => {
          if (debounce) clearTimeout(debounce);
          debounce = setTimeout(() => reconcile(scope.tokens, scope.tenantWide), ALARM_RECONCILE_DEBOUNCE_MS);
        };
        // Subscribe unfiltered (server filters resolve once at subscribe time and a
        // widget's scope may span devices) and treat every event as a reconcile
        // trigger — the query re-applies the scope+filters. On reconnect, re-query to
        // catch transitions missed while the socket was down.
        const handle = this.connection.register();
        const adapter: SubscriptionSink<AlarmStreamResult> = {
          next: () => trigger(),
          connected: (wasRetry) => {
            handle.connected();
            if (wasRetry) reconcile(scope.tokens, scope.tenantWide);
          },
          closed: () => handle.closed(),
        };
        const closeSocket = subscribe(DEVICE_AREA, ALARM_STREAM, {}, adapter);
        unsubscribe = () => {
          handle.release();
          closeSocket();
        };
        poll = setInterval(() => reconcile(scope.tokens, scope.tenantWide), ALARM_POLL_MS);
        // Register a reconcile trigger so an ack/clear (via the action seam) refreshes
        // this widget immediately, not on the next poll tick.
        reconciler = () => reconcile(scope.tokens, scope.tenantWide);
        this.alarmReconcilers.add(reconciler);
        reconcile(scope.tokens, scope.tenantWide); // initial load
      })
      .catch((err) => {
        if (!disposed) sink.error?.(err);
      });

    return dispose;
  }

  // resolveAlarmScope turns an alarm widget's scope selector into the originator device
  // tokens to filter on, or tenant-wide when it carries no datasource. Reuses the same
  // device/anchor/unbound resolution the measurement channel does.
  //
  // 🔴 Only an ABSENT datasource is tenant-wide. `{kind:'unbound'}` is a datasource, and
  // it resolves to zero devices like any other empty scope.
  private async resolveAlarmScope(
    datasource: DatasourceSelector | undefined,
  ): Promise<{ tenantWide: boolean; tokens: string[] }> {
    if (!datasource) return { tenantWide: true, tokens: [] };
    const groups = await this.resolveDevices(datasource);
    return { tenantWide: false, tokens: groups.map((g) => g.deviceToken) };
  }

  // queryAlarms reads the authoritative rows. Tenant-wide is one query; a scoped widget
  // runs one query per originator device (the alarms query filters a single originator)
  // and merges — deduped by token, newest first, capped to pageSize; total is the sum of
  // per-originator match counts.
  private async queryAlarms(
    sub: AlarmSubscription,
    tokens: string[],
    tenantWide: boolean,
  ): Promise<AlarmSnapshot> {
    const filters = {
      state: sub.state ?? null,
      severity: sub.severity ?? null,
      acknowledged: sub.acknowledged ?? null,
    } satisfies Partial<AlarmSearchCriteriaInput>;
    const originators: Array<{ originatorType: string | null; originator: string | null }> = tenantWide
      ? [{ originatorType: null, originator: null }]
      : tokens.map((token) => ({ originatorType: 'device', originator: token }));

    if (sub.countBySeverity) {
      // One grouped read per originator query (one tenant-wide), summed by severity. Per-device
      // reads cannot double count: an alarm has one originator.
      const pages = await Promise.all(
        originators.map((o) =>
          gql(DEVICE_AREA, ALARM_SEVERITY_COUNTS, { criteria: { ...filters, ...o }, deviceType: sub.deviceType ?? null }),
        ),
      );
      const merged = new Map<string, number>();
      for (const page of pages) {
        for (const c of page.alarmCountsBySeverity) merged.set(c.severity, (merged.get(c.severity) ?? 0) + c.count);
      }
      const severityCounts = [...merged].map(([severity, count]) => ({ severity, count }));
      return { alarms: [], total: severityCounts.reduce((n, c) => n + c.count, 0), severityCounts };
    }

    if (sub.countByDeviceType) {
      // One grouped read per originator query (one tenant-wide), summed by (type, severity).
      const pages = await Promise.all(
        originators.map((o) => gql(DEVICE_AREA, ALARM_TYPE_COUNTS, { criteria: { ...filters, ...o } })),
      );
      const merged = new Map<string, AlarmTypeCount>();
      for (const page of pages) {
        for (const c of page.alarmCountsByDeviceType) {
          const key = `${c.deviceType.token}\u0000${c.severity}`;
          const prior = merged.get(key);
          merged.set(key, prior ? { ...prior, count: prior.count + c.count } : { ...c, deviceType: { ...c.deviceType } });
        }
      }
      const typeCounts = [...merged.values()];
      return { alarms: [], total: typeCounts.reduce((n, c) => n + c.count, 0), typeCounts };
    }

    // The type filter is device-management's, so a page is the subscription's either way.
    const base = {
      pageNumber: 1, // the alarms query paginates 1-based
      pageSize: sub.pageSize,
      ...filters,
      deviceType: sub.deviceType ?? null,
      ...windowCriteria(sub.window, Date.now()),
    } satisfies Partial<AlarmSearchCriteriaInput>;
    const pages = await Promise.all(
      originators.map((o) => gql(DEVICE_AREA, ALARMS_QUERY, { criteria: { ...base, ...o } })),
    );
    const byToken = new Map<string, AlarmRow>();
    let total = 0;
    for (const page of pages) {
      total += page.alarms.pagination.totalRecords;
      for (const row of page.alarms.results) byToken.set(row.token, row);
    }
    let alarms = [...byToken.values()]
      .sort((a, b) => (b.raisedTime ?? '').localeCompare(a.raisedTime ?? ''))
      .slice(0, sub.pageSize);
    if (sub.enrichDeviceType === true) alarms = await this.withDeviceTypes(alarms);
    return { alarms, total };
  }

  // Each device's type, for the lifetime of the hub: a device's type is fixed for a viewing session,
  // and the same originators come back every poll. ONE devicesByToken read per chunk of uncached
  // tokens (the server takes 1000), never one per alarm. A failed read leaves those rows with no
  // `deviceType` (unknown, so a grouping widget lists them under All only) instead of failing the
  // whole alarm table, and is retried on the next reconcile because a failure is not cached.
  private readonly alarmDeviceTypes = new Map<string, AlarmDeviceType>();

  private async withDeviceTypes(rows: AlarmRow[]): Promise<AlarmRow[]> {
    const missing = [
      ...new Set(
        rows
          .filter((r) => r.originatorType === 'device' && r.originatorToken && !this.alarmDeviceTypes.has(r.originatorToken))
          .map((r) => r.originatorToken as string),
      ),
    ];
    for (let i = 0; i < missing.length; i += ALARM_DEVICE_TYPES_CHUNK) {
      try {
        const r = await gql(DEVICE_AREA, ALARM_DEVICE_TYPES, { tokens: missing.slice(i, i + ALARM_DEVICE_TYPES_CHUNK) });
        for (const d of r.devicesByToken) this.alarmDeviceTypes.set(d.token, d.deviceType);
      } catch {
        // rows stay unresolved; see above
      }
    }
    return rows.map((r) => {
      if (r.originatorType !== 'device' || !r.originatorToken) return { ...r, deviceType: null };
      const deviceType = this.alarmDeviceTypes.get(r.originatorToken);
      return deviceType ? { ...r, deviceType } : r;
    });
  }

  // ── Control channel ──────────────────────────────────────────────────────

  // subscribeCommands binds a command widget's scope to a sink and returns a disposer.
  // Poll-only (command-delivery has no subscription): resolve the target device once,
  // then re-read its recent commands on an interval (and immediately after an issue via
  // the reconciler). Scope resolution is async; the disposer is returned synchronously
  // and cancels a still-pending resolution.
  subscribeCommands(subscription: CommandSubscription, sink: CommandStreamSink): () => void {
    let disposed = false;
    let poll: ReturnType<typeof setInterval> | undefined;
    let reconciler: (() => void) | undefined;
    let deviceToken: string | null = null;
    // Monotonic generation: a slow poll that resolves after a newer one can't overwrite
    // fresher rows.
    let generation = 0;

    const dispose = (): void => {
      disposed = true;
      if (poll) clearInterval(poll);
      if (reconciler) this.commandReconcilers.delete(reconciler);
      this.commandDisposers.delete(dispose);
    };
    this.commandDisposers.add(dispose);

    const reconcile = (): void => {
      const gen = ++generation;
      this.queryCommands(subscription, deviceToken)
        .then((snapshot) => {
          if (!disposed && gen === generation) sink.next(snapshot);
        })
        .catch((err) => {
          if (!disposed && gen === generation) sink.error?.(err);
        });
    };

    this.resolveCommandScope(subscription.datasource)
      .then((token) => {
        if (disposed) return;
        deviceToken = token;
        // A command button needs a single target device. Unscoped/unresolved (an unbound
        // slot, no device) → an empty state, NOT tenant-wide: a command can't be issued
        // to "all devices". Nothing to poll.
        if (!deviceToken) {
          sink.next({ deviceToken: null, commands: [], total: 0 });
          return;
        }
        poll = setInterval(reconcile, COMMAND_POLL_MS);
        reconciler = reconcile;
        this.commandReconcilers.add(reconciler);
        reconcile(); // initial load
      })
      .catch((err) => {
        if (!disposed) sink.error?.(err);
      });

    return dispose;
  }

  // resolveCommandScope turns a command widget's scope selector into its single target
  // device token (a command targets one device), or null when it carries no datasource
  // or resolves to no device. When a selector expands to several devices (an anchor), the
  // first is the target — the console restricts command widgets to a device scope, so
  // this is a defensive fallback, not the authoring path.
  private async resolveCommandScope(
    datasource: DatasourceSelector | undefined,
  ): Promise<string | null> {
    if (!datasource) return null;
    const groups = await this.resolveDevices(datasource);
    return groups[0]?.deviceToken ?? null;
  }

  // queryCommands reads the recent commands for the target device (newest first, capped
  // to pageSize) with their live delivery status.
  private async queryCommands(
    sub: CommandSubscription,
    deviceToken: string | null,
  ): Promise<CommandSnapshot> {
    if (!deviceToken) return { deviceToken: null, commands: [], total: 0 };
    const data = await gql(COMMAND_AREA, COMMANDS_QUERY, {
      criteria: { pageNumber: 1, pageSize: sub.pageSize, deviceToken, status: null, ...windowCriteria(sub.window, Date.now()) },
    });
    return {
      deviceToken,
      commands: data.commands.results,
      total: data.commands.pagination.totalRecords,
    };
  }

  // ── Location channel ─────────────────────────────────────────────────────

  // subscribeLocations binds a map widget's selector to a sink and returns a disposer.
  // Poll-only (device-state has no location subscription): resolve the bound devices
  // once, then re-read their last-known positions on an interval. Scope resolution is
  // async; the disposer is returned synchronously and cancels a still-pending
  // resolution, matching every other channel.
  subscribeLocations(subscription: LocationSubscription, sink: LocationStreamSink): () => void {
    let disposed = false;
    let poll: ReturnType<typeof setInterval> | undefined;
    let deviceTokens: string[] = [];
    // Monotonic generation: a slow poll that resolves after a newer one can't overwrite
    // fresher positions.
    let generation = 0;

    const dispose = (): void => {
      disposed = true;
      if (poll) clearInterval(poll);
      this.locationDisposers.delete(dispose);
    };
    this.locationDisposers.add(dispose);

    const reconcile = (): void => {
      const gen = ++generation;
      this.queryLocations(deviceTokens)
        .then((snapshot) => {
          if (!disposed && gen === generation) sink.next(snapshot);
        })
        .catch((err) => {
          if (!disposed && gen === generation) sink.error?.(err);
        });
    };

    this.resolveLocationScope(subscription.datasource)
      .then((tokens) => {
        if (disposed) return;
        deviceTokens = tokens;
        // A widget that names no location series, carries no datasource, or resolves to
        // no device has nothing to place on a map. Emit the empty POSITIONS snapshot —
        // never `forbidden`, which would claim a permission problem that does not
        // exist — and open no poll.
        if (deviceTokens.length === 0) {
          sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
          return;
        }
        poll = setInterval(reconcile, LOCATION_POLL_MS);
        reconcile(); // initial load
      })
      .catch((err) => {
        if (!disposed) sink.error?.(err);
      });

    return dispose;
  }

  // resolveLocationScope turns a map widget's selector into the device tokens whose
  // positions to read.
  //
  // 🔴 It resolves NOTHING unless the selector NAMES A LOCATION SERIES. That is the
  // point of the separate field: a device selector carrying only `measurements` is a
  // telemetry binding, and quietly reading its device's position because a map widget
  // happens to hold it would make the location field decorative — authored or not, the
  // behaviour would be identical, so nothing would ever hold it. A map bound to a
  // measurement-only selector shows its empty state, which is the honest answer.
  private async resolveLocationScope(
    datasource: DatasourceSelector | undefined,
  ): Promise<string[]> {
    if (!datasource?.location) return [];
    const groups = await this.resolveDevices(datasource);
    return groups.map((g) => g.deviceToken);
  }

  // queryLocations reads the last-known position of each bound device in ONE batch
  // round trip. A device that has never been located is absent from the result (the
  // service's contract), so the caller reads "how many are located" from the returned
  // rows and "how many are bound" from the tokens.
  private async queryLocations(deviceTokens: string[]): Promise<LocationSnapshot> {
    if (deviceTokens.length === 0) return { kind: 'positions', deviceTokens, locations: [] };
    try {
      const data = await gql(STATE_AREA, LATEST_LOCATIONS_QUERY, { deviceTokens });
      return { kind: 'positions', deviceTokens, locations: data.latestLocations };
    } catch (err) {
      // Only a REFUSAL becomes a value; every other failure stays a failure, so a broken
      // device-state is never dressed up as a permission boundary (and vice versa — a
      // permission boundary is never dressed up as an outage the operator should page
      // someone about).
      if (isForbiddenError(err)) return { kind: 'forbidden' };
      throw err;
    }
  }

  // subscribePresence: the roster, then the roster's device-state on an interval. Poll-only (see
  // PRESENCE_POLL_MS). A refusal (`state:read` is outside some members' grants) becomes `forbidden`,
  // and every other failure stays an error, as for the location channel. A slow read that lands after
  // a newer one is dropped, so an old answer never overwrites a fresher one.
  subscribePresence(datasource: DatasourceSelector, sink: PresenceSink): () => void {
    let disposed = false;
    let poll: ReturnType<typeof setInterval> | undefined;
    let generation = 0;
    const dispose = (): void => {
      disposed = true;
      if (poll) clearInterval(poll);
      this.presenceDisposers.delete(dispose);
    };
    this.presenceDisposers.add(dispose);

    const reconcile = (tokens: string[]): void => {
      const gen = ++generation;
      this.queryPresence(tokens).then(
        (states) => {
          if (!disposed && gen === generation) sink.next(states);
        },
        (err) => {
          if (disposed || gen !== generation) return;
          if (isForbiddenError(err)) sink.forbidden?.();
          else sink.error?.(err);
        },
      );
    };

    this.resolveRoster(datasource).then(
      ({ roster }) => {
        if (disposed) return;
        sink.roster(roster);
        const tokens = roster.members.map((m) => m.token);
        if (tokens.length === 0) {
          sink.next([]);
          return;
        }
        poll = setInterval(() => reconcile(tokens), PRESENCE_POLL_MS);
        reconcile(tokens);
      },
      (err) => {
        if (disposed) return;
        if (isForbiddenError(err)) sink.forbidden?.();
        else sink.error?.(err);
      },
    );
    return dispose;
  }

  private async queryPresence(tokens: string[]): Promise<DevicePresence[]> {
    const chunks: string[][] = [];
    for (let i = 0; i < tokens.length; i += PRESENCE_CHUNK) chunks.push(tokens.slice(i, i + PRESENCE_CHUNK));
    const reads = await Promise.all(chunks.map((deviceTokens) => gql(STATE_AREA, DEVICE_PRESENCE_QUERY, { deviceTokens })));
    return reads.flatMap((r) => r.deviceStatesByDeviceToken);
  }

  // ── WidgetActions (the write seam) ───────────────────────────────────────

  // can reports whether the viewer holds an authority ('*' grants all). Drives whether
  // a widget renders an action control; the server enforces authority regardless.
  can(authority: string): boolean {
    return this.authorities.has('*') || this.authorities.has(authority);
  }

  // acknowledgeAlarm / clearAlarm mutate the alarm by token, then nudge every open alarm
  // widget to reconcile so the change shows at once. The mutation reaches device-management
  // (the acknowledging identity is taken server-side from the token).
  async acknowledgeAlarm(alarmToken: string): Promise<void> {
    await gql(DEVICE_AREA, ACKNOWLEDGE_ALARM, { token: alarmToken });
    this.reconcileAlarms();
  }

  async clearAlarm(alarmToken: string): Promise<void> {
    await gql(DEVICE_AREA, CLEAR_ALARM, { token: alarmToken });
    this.reconcileAlarms();
  }

  // sendCommand issues a command to a device, minting the dispatch token here (the
  // idempotency key + cancel handle), then nudges every open command widget to reconcile
  // so the new command shows at once. The mutation reaches command-delivery (requires
  // command:write, enforced server-side regardless of can()).
  //
  // A REFUSAL COMES BACK AS A VALUE, not an exception (see CommandDispatch): the server
  // decided the request and named the reason, so the widget can show it. Nothing was
  // created in that case, so the open command widgets are NOT reconciled — a re-poll
  // would only re-render the same history and make a refused send look like it did
  // something.
  async sendCommand(deviceToken: string, name: string, payload?: string): Promise<CommandDispatch> {
    const token = randomToken();
    const result = await gql(COMMAND_AREA, CREATE_COMMAND, {
      request: { token, deviceToken, name, payload: payload ?? null },
    });
    const rejection = result.createCommand?.rejection;
    if (rejection) {
      return { status: 'rejected', code: rejection.code, reason: rejection.reason };
    }
    // 🔴 A response carrying NEITHER arm is a contract violation, and it must not read
    // as success. Exactly one of command/rejection is populated; neither means the
    // answer never arrived intact — a resolver returning nothing, a host GraphQL layer
    // dropping a field, a mangled response.
    //
    // The trap is that the dispatch token is minted HERE, so nothing forces this code
    // to look at the server's answer at all: without this guard the fall-through
    // returns { status: 'sent' } with a token that names no command, and reconciles
    // every open command widget as though something had been created. The operator is
    // told their command went out and has a handle that cancels nothing.
    //
    // The two siblings written alongside this both guard it — the REACT sink returns an
    // explicit error, the console throws — and this is the surface most likely to be
    // embedded outside the console, so it is the last one that should be lenient.
    if (!result.createCommand?.command) {
      throw new Error('The command could not be issued: the platform returned no answer for it.');
    }
    this.reconcileCommands();
    return { status: 'sent', token };
  }

  // reconcileAlarms re-queries every open alarm subscription hub-wide (after a mutation).
  // Hub-wide is deliberate: one alarm can appear in several widgets (different scopes),
  // and the acked/cleared row must refresh in all of them; scoping the nudge would need
  // per-reconciler token knowledge for no real saving. Iterate a copy for safety.
  private reconcileAlarms(): void {
    for (const reconcile of [...this.alarmReconcilers]) reconcile();
  }

  // reconcileCommands re-polls every open command subscription (after an issue). Iterate
  // a copy for safety.
  private reconcileCommands(): void {
    for (const reconcile of [...this.commandReconcilers]) reconcile();
  }

  // disposeAll tears down every upstream stream (e.g. on dashboard close): the
  // ref-counted measurement device streams AND every alarm/command/location
  // subscription's poll + trigger. Iterate a copy of the disposer sets since each
  // removes itself as it runs.
  disposeAll(): void {
    for (const stream of this.streams.values()) stream.unsubscribe();
    this.streams.clear();
    for (const stream of this.deadStreams) stream.unsubscribe();
    this.deadStreams.clear();
    for (const dispose of [...this.alarmDisposers]) dispose();
    this.alarmDisposers.clear();
    for (const dispose of [...this.commandDisposers]) dispose();
    this.commandDisposers.clear();
    for (const dispose of [...this.locationDisposers]) dispose();
    this.locationDisposers.clear();
    for (const dispose of [...this.presenceDisposers]) dispose();
    this.presenceDisposers.clear();
  }

  // The number of distinct upstream device streams currently open (observability
  // + test hook — proves multiplexing collapses shared devices to one stream).
  get openStreamCount(): number {
    return this.streams.size;
  }

  // isDatasourceAvailable reports whether a widget's bound device still exists. Only a
  // device selector is validated (a slot bound to a device arrives as one) — an anchor, an
  // unbound slot, or no datasource has a legitimate empty state and is always "available".
  // Fails open: an existence-check outage returns true (never falsely mark a live device
  // gone).
  async isDatasourceAvailable(datasource: DatasourceSelector | undefined): Promise<boolean> {
    const deviceToken = this.availabilityToken(datasource);
    if (deviceToken === undefined) return true;
    try {
      return await this.resolver.deviceExists(deviceToken);
    } catch {
      return true;
    }
  }

  // isDatasourceSupported reports whether the viewer is offered the binding at all: false only
  // when resolving an anchor is refused as SCOPE_UNSUPPORTED (an area or asset anchor, for a
  // customer user). The resolution is the same memoized one the widget's channel makes, so a
  // supported anchor costs no second read. Every other answer, including a failure, is true.
  async isDatasourceSupported(datasource: DatasourceSelector | undefined): Promise<boolean> {
    if (datasource?.kind !== 'anchor') return true;
    try {
      await this.resolver.devicesForAnchor(datasource.anchor);
      return true;
    } catch (err) {
      return !isScopeUnsupportedError(err);
    }
  }

  // availabilityToken returns the single device token whose existence gates a widget's
  // availability, or undefined when there is nothing device-specific to validate (an
  // anchor's membership is self-validating; an unbound slot is a placeholder; a reserved
  // kind isn't resolved yet; an unresolved slot fails on its data channel instead).
  private availabilityToken(datasource: DatasourceSelector | undefined): string | undefined {
    if (!datasource) return undefined;
    // An empty token (a half-authored or hand-edited definition) has nothing to
    // validate — treat it like an unbound slot (available/empty), not a device that
    // "no longer exists", and skip the guaranteed-empty query.
    if (datasource.kind === 'device') return datasource.deviceToken || undefined;
    return undefined;
  }

  // resolveDevices turns a selector into the devices to stream, each with the
  // measurement names the widget wants (empty = all). Reserved selector kinds are
  // rejected here, mirroring the backend (Phase 1 ships device + anchor).
  private async resolveDevices(
    datasource: DatasourceSelector,
  ): Promise<Array<{ deviceToken: string; names: Set<string> }>> {
    switch (datasource.kind) {
      case 'device':
        return this.resolveBinding(
          { kind: 'device', deviceToken: datasource.deviceToken },
          new Set(datasource.measurements),
        );
      case 'anchor':
        return this.resolveBinding(
          { kind: 'anchor', anchor: datasource.anchor },
          new Set(datasource.measurements),
        );
      case 'unbound':
        // A slot with no binding, as the renderer resolved it: a valid placeholder (a
        // template the host hasn't bound, a selection not yet made), not an error.
        // Zero devices — a silent empty state, like an anchor with no members.
        return [];
      case 'slot':
        // 🔴 FAIL LOUDLY. The hub keeps no bindings, so a slot here was never resolved:
        // the host handed a raw definition widget to the data source instead of going
        // through the renderer (or resolveWidgetDatasource). Answering "zero devices"
        // would make that bug look exactly like a slot that is legitimately unbound.
        throw new Error(
          `dashboard slot '${datasource.slot}' reached the data source unresolved; ` +
            'resolve it through the bindings first (resolveWidgetDatasource)',
        );
      default:
        throw new Error(
          `dashboard selector kind '${datasource.kind}' is not supported yet`,
        );
    }
  }

  // resolveBinding turns a concrete entity binding (device or anchor) into the
  // device streams to open, each carrying the given measurement names. Shared by the
  // device/anchor selectors and by slot resolution (whose binding is either kind).
  // A device binding streams its token directly (measurementStream is keyed by token,
  // per ADR-044); an anchor expands to its member device tokens.
  private async resolveBinding(
    binding: SlotBinding,
    names: Set<string>,
  ): Promise<Array<{ deviceToken: string; names: Set<string> }>> {
    if (binding.kind === 'device') {
      return [{ deviceToken: binding.deviceToken, names }];
    }
    const tokens = await this.resolver.devicesForAnchor(binding.anchor);
    return tokens.map((deviceToken) => ({ deviceToken, names }));
  }

  // attach registers a subscriber on a device's stream (opening the upstream on
  // the first subscriber) and returns a detacher that drops it and closes the
  // upstream once the last subscriber leaves.
  private attach(deviceToken: string, names: Set<string>, sink: WidgetStreamSink): () => void {
    const stream = this.ensureStream(deviceToken);
    const subscriber: Subscriber = { names, sink };
    stream.subscribers.add(subscriber);

    return () => {
      if (!stream.subscribers.delete(subscriber)) return;
      // Only tear down and forget the stream if it is STILL the registered stream
      // for this device — an upstream error may have evicted and replaced it, and a
      // lingering old subscriber's detach must not delete the replacement.
      if (stream.subscribers.size === 0) {
        // An evicted stream's operation is already closed; this releases its connection
        // handle, which it kept so a dead socket reads as down while anything still shows it.
        stream.unsubscribe();
        this.deadStreams.delete(stream);
        if (this.streams.get(deviceToken) === stream) this.streams.delete(deviceToken);
      }
    };
  }

  private ensureStream(deviceToken: string): DeviceStream {
    const existing = this.streams.get(deviceToken);
    if (existing) return existing;

    const stream: DeviceStream = { subscribers: new Set(), unsubscribe: () => {} };
    // Register before subscribing so that even a synchronously-delivered first
    // sample resolves through fanout (unsubscribe stays the no-op placeholder only
    // for the brief window until subscribe() returns the real disposer).
    this.streams.set(deviceToken, stream);

    // Subscribe unfiltered by name (name: null) so a device's every reading rides
    // ONE upstream and each subscriber filters to the names it wants — a chart and
    // a card on the same device share the stream instead of opening two.
    const handle = this.connection.register();
    let closeSocket: (() => void) | undefined;
    let operationOpen = true;
    // Consecutive server-side ends with no reading in between; spaces the re-opens below.
    let ended = 0;
    // This operation's socket is up, as far as its last connected/closed said.
    let up = true;
    let reopen: ReturnType<typeof setTimeout> | undefined;
    const closeOperation = (): void => {
      if (!operationOpen) return;
      operationOpen = false;
      clearTimeout(reopen);
      closeSocket?.();
    };
    const open = (): void => {
      closeSocket = subscribe(EVENT_AREA, MEASUREMENT_STREAM, variables, adapter);
    };
    // The stream resumed after an interruption: whatever was sent meanwhile was never delivered,
    // and each subscriber is told so it can read the missed history back. Sent to the stream's own
    // subscribers. Never called once the operation is closed: closing it drops its connection
    // listener and clears a pending re-open.
    const resumed = (): void => {
      stream.subscribers.forEach((s) => s.sink.reconnected?.());
    };
    const adapter: SubscriptionSink<MeasurementStreamResult> = {
      next: (data) => {
        ended = 0;
        this.fanout(deviceToken, data.measurementStream);
      },
      connected: (wasRetry) => {
        up = true;
        handle.connected();
        // graphql-ws re-sends this operation on the new socket; a first connect is no gap.
        if (wasRetry) resumed();
      },
      closed: () => {
        up = false;
        handle.closed();
      },
      // 🔴 The server ended this operation on a socket that is still up: its live source
      // closed, or could not be opened (a resolver error reaches the client as an error-only
      // `next`, which carries no data, and then this). Left alone, the stream would sit here
      // receiving nothing while the board read `live` -- a widget frozen until someone reloads,
      // one stream at a time. Open it again, after a doubling wait capped at 30 s, for as long
      // as anything shows it. Two other completes are not the server's and are never re-opened:
      // a widget's own unsubscribe (closeOperation has marked it), and the client being disposed
      // on sign-out, which closes the socket first (re-opening would bring back the socket the
      // sign-out exists to drop).
      complete: () => {
        if (!operationOpen || !up) return;
        closeSocket?.();
        reopen = setTimeout(() => {
          open();
          // Opened on a socket that is already up, so no connection_ack follows to say so.
          resumed();
        }, Math.min(1000 * 2 ** ended++, 30_000));
      },
      error: (err) => {
        // The upstream is dead. Evict it (and drop the socket-level subscription)
        // so the NEXT subscriber for this device opens a fresh stream instead of
        // attaching to this corpse and freezing silently — the reconnect path.
        // Guard the delete so a stream that has already been replaced is left be.
        if (this.streams.get(deviceToken) === stream) this.streams.delete(deviceToken);
        // 🔴 Close the operation but KEEP the connection handle until the last subscriber
        // detaches. A dropped socket is retried for as long as the stream is wanted (the
        // client never gives up on one), so what lands here is final: a close that says
        // the request is wrong (a refused token among them) or the server ending this
        // operation. A fatal close errors every operation at once, so releasing here would
        // empty the tracker exactly when the socket is down for good, and a board whose
        // every stream had died would read `live`. A handle
        // whose socket was up when the operation failed (a server-side error) counts as up.
        closeOperation();
        if (stream.subscribers.size > 0) this.deadStreams.add(stream);
        else handle.release();
        for (const subscriber of stream.subscribers) subscriber.sink.error?.(err);
      },
    };
    const variables: MeasurementStreamVariables = { deviceToken, name: null };
    open();
    if (!operationOpen) closeSocket?.(); // it failed while subscribing
    stream.unsubscribe = () => {
      handle.release();
      closeOperation();
    };

    return stream;
  }

  private fanout(deviceToken: string, sample: MeasurementSample): void {
    const stream = this.streams.get(deviceToken);
    if (!stream) return;
    for (const subscriber of stream.subscribers) {
      if (subscriber.names.size === 0 || subscriber.names.has(sample.name)) {
        subscriber.sink.next(sample);
      }
    }
  }
}
