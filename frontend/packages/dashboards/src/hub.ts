// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardHub — the dashboard runtime (ADR-039).
//
// It multiplexes every widget's live telemetry over the SDK's single per-area
// graphql-ws connection: many widgets bound to the same device share ONE upstream
// measurementStream subscription (ref-counted), so a crowded dashboard opens one
// stream per distinct device token, not one per widget — the per-widget-subscription
// fan-out ThingsBoard collapses under (#3454). The Hub owns the subscription
// lifecycle; widgets just hand it a datasource selector and a sink.

import { gql, subscribe, type Area, type SubscriptionSink } from '@devicechain/client';

import {
  ACKNOWLEDGE_ALARM,
  ALARM_STREAM,
  ALARMS_QUERY,
  CLEAR_ALARM,
  type AlarmSearchCriteriaInput,
  type AlarmStreamResult,
} from './internal/alarm-doc';
import {
  COMMANDS_QUERY,
  CREATE_COMMAND,
} from './internal/command-doc';
import {
  MEASUREMENT_STREAM,
  type MeasurementStreamResult,
  type MeasurementStreamVariables,
} from './internal/measurement-doc';
import type {
  AlarmRow,
  AnchorTarget,
  CommandRow,
  DatasourceSelector,
  MeasurementSample,
  SlotBinding,
} from './types';

const EVENT_AREA: Area = 'event-management';
const DEVICE_AREA: Area = 'device-management';
const COMMAND_AREA: Area = 'command-delivery';

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

// Command channel cadence. command-delivery exposes NO subscription, so the control
// channel is poll-only — but a command's lifecycle (QUEUED→SENT→DELIVERED→SUCCESSFUL)
// resolves in seconds, so it polls far faster than the alarm channel. An issued command
// reconciles immediately (not on the next tick) so the operator sees it appear at once.
const COMMAND_POLL_MS = 4_000;

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
}

// WidgetStreamSink receives live samples for one widget, across every device its
// datasource resolves to. next() fires per sample; error() once if selector
// resolution or the socket fails.
export interface WidgetStreamSink {
  next: (sample: MeasurementSample) => void;
  error?: (err: unknown) => void;
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
}

// A full alarm snapshot: the current rows (newest first, capped to pageSize) and the
// total number of alarms matching the filter (past the page). One replaces the last.
export interface AlarmSnapshot {
  alarms: AlarmRow[];
  total: number;
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

// The result of issuing a command: its freshly-minted dispatch token, so the widget can
// highlight the command it just sent as it moves through the lifecycle.
export interface CommandDispatch {
  token: string;
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
  // appears at once. OPTIONAL: added after this interface shipped, so a host predating it
  // (or a strictly read-only one) may omit it — a command widget then renders its Send
  // control disabled. `payload` is the request body sent to the device verbatim (the
  // widget serializes its typed parameter form to JSON).
  sendCommand?(deviceToken: string, name: string, payload?: string): Promise<CommandDispatch>;
  // Whether the current viewer holds an authority (e.g. 'alarm:write'). Drives whether
  // an action control renders at all.
  can(authority: string): boolean;
}

export interface DashboardHubConfig {
  resolver: DeviceResolver;
  // The effective slot→entity manifest (slot defaults merged with any host override;
  // see effectiveBindings). A widget's `slot` selector resolves through this. Absent
  // slots render as an empty placeholder. Can be replaced later via setBindings.
  bindings?: Record<string, SlotBinding>;
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
  // Whether a widget's bound entity still exists. Optimistic + async: a widget renders
  // from its stream immediately, and this resolves separately — only a device selector
  // (or a slot bound to a device) whose token no longer resolves reports false, so the
  // widget shows "unavailable" instead of a blank pane. Anchor / unbound-slot / no-
  // datasource report true (their empty state is legitimate, not "unavailable"). Fails
  // OPEN (true) on an inconclusive check, so a device-management blip never falsely marks
  // a live device unavailable.
  isDatasourceAvailable(datasource: DatasourceSelector | undefined): Promise<boolean>;
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
  // slot name → concrete entity binding. Consulted when a widget's selector is a
  // `slot`. Mutable so the authoring host can rebind live (setBindings).
  private bindings: Record<string, SlotBinding>;

  constructor(config: DashboardHubConfig) {
    this.resolver = config.resolver;
    this.bindings = config.bindings ?? {};
    this.authorities = new Set(config.authorities ?? []);
  }

  // setBindings replaces the slot manifest. New subscriptions resolve through it;
  // callers that need already-open slot streams to re-resolve should re-subscribe
  // (the console keys the renderer on the manifest to do exactly that).
  setBindings(bindings: Record<string, SlotBinding>): void {
    this.bindings = bindings;
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
        // the measurement channel): a slot rebind rebuilds the hub and re-resolves, but
        // organic anchor-membership change isn't picked up until the hub is rebuilt —
        // a deferred enhancement shared with the measurement channel.
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
        const adapter: SubscriptionSink<AlarmStreamResult> = {
          next: () => trigger(),
          connected: (wasRetry) => {
            if (wasRetry) reconcile(scope.tokens, scope.tenantWide);
          },
        };
        unsubscribe = subscribe(DEVICE_AREA, ALARM_STREAM, {}, adapter);
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
  // device/anchor/slot resolution the measurement channel does.
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
    const base = {
      pageNumber: 1, // the alarms query paginates 1-based
      pageSize: sub.pageSize,
      state: sub.state ?? null,
      severity: sub.severity ?? null,
      acknowledged: sub.acknowledged ?? null,
    } satisfies Partial<AlarmSearchCriteriaInput>;

    if (tenantWide) {
      const data = await gql(DEVICE_AREA, ALARMS_QUERY, {
        criteria: { ...base, originatorType: null, originator: null },
      });
      return { alarms: data.alarms.results, total: data.alarms.pagination.totalRecords };
    }

    const pages = await Promise.all(
      tokens.map((token) =>
        gql(DEVICE_AREA, ALARMS_QUERY, {
          criteria: { ...base, originatorType: 'device', originator: token },
        }),
      ),
    );
    const byToken = new Map<string, AlarmRow>();
    let total = 0;
    for (const page of pages) {
      total += page.alarms.pagination.totalRecords;
      for (const row of page.alarms.results) byToken.set(row.token, row);
    }
    const alarms = [...byToken.values()]
      .sort((a, b) => (b.raisedTime ?? '').localeCompare(a.raisedTime ?? ''))
      .slice(0, sub.pageSize);
    return { alarms, total };
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
      criteria: { pageNumber: 1, pageSize: sub.pageSize, deviceToken, status: null },
    });
    return {
      deviceToken,
      commands: data.commands.results,
      total: data.commands.pagination.totalRecords,
    };
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
  async sendCommand(deviceToken: string, name: string, payload?: string): Promise<CommandDispatch> {
    const token = randomToken();
    await gql(COMMAND_AREA, CREATE_COMMAND, {
      request: { token, deviceToken, name, payload: payload ?? null },
    });
    this.reconcileCommands();
    return { token };
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
  // ref-counted measurement device streams AND every alarm/command subscription's poll +
  // trigger. Iterate a copy of the disposer sets since each removes itself as it runs.
  disposeAll(): void {
    for (const stream of this.streams.values()) stream.unsubscribe();
    this.streams.clear();
    for (const dispose of [...this.alarmDisposers]) dispose();
    this.alarmDisposers.clear();
    for (const dispose of [...this.commandDisposers]) dispose();
    this.commandDisposers.clear();
  }

  // The number of distinct upstream device streams currently open (observability
  // + test hook — proves multiplexing collapses shared devices to one stream).
  get openStreamCount(): number {
    return this.streams.size;
  }

  // isDatasourceAvailable reports whether a widget's bound device still exists. Only a
  // device selector (or a slot bound to a device) is validated — an anchor, an unbound
  // slot, or no datasource has a legitimate empty state and is always "available". Fails
  // open: an existence-check outage returns true (never falsely mark a live device gone).
  async isDatasourceAvailable(datasource: DatasourceSelector | undefined): Promise<boolean> {
    const deviceToken = this.availabilityToken(datasource);
    if (deviceToken === undefined) return true;
    try {
      return await this.resolver.deviceExists(deviceToken);
    } catch {
      return true;
    }
  }

  // availabilityToken returns the single device token whose existence gates a widget's
  // availability, or undefined when there is nothing device-specific to validate (an
  // anchor's membership is self-validating; an unbound slot is a placeholder; a reserved
  // kind isn't resolved yet).
  private availabilityToken(datasource: DatasourceSelector | undefined): string | undefined {
    if (!datasource) return undefined;
    // An empty token (a half-authored or hand-edited definition) has nothing to
    // validate — treat it like an unbound slot (available/empty), not a device that
    // "no longer exists", and skip the guaranteed-empty query.
    if (datasource.kind === 'device') return datasource.deviceToken || undefined;
    if (datasource.kind === 'slot') {
      const binding = Object.prototype.hasOwnProperty.call(this.bindings, datasource.slot)
        ? this.bindings[datasource.slot]
        : undefined;
      return binding && binding.kind === 'device' ? binding.deviceToken || undefined : undefined;
    }
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
      case 'slot': {
        // Own-property lookup: a slot named 'constructor'/'__proto__'/'toString' must
        // NOT resolve to an inherited Object.prototype member (which is truthy and
        // would bypass the unbound-placeholder guard, then crash on binding.kind).
        // An unbound slot is a valid placeholder (a template the host hasn't bound),
        // not an error — resolve to zero devices, a silent empty state (like an anchor
        // with no members), so the widget shows an empty pane, not an error.
        const binding = Object.prototype.hasOwnProperty.call(this.bindings, datasource.slot)
          ? this.bindings[datasource.slot]
          : undefined;
        if (!binding) return [];
        return this.resolveBinding(binding, new Set(datasource.measurements));
      }
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
      if (stream.subscribers.size === 0 && this.streams.get(deviceToken) === stream) {
        stream.unsubscribe();
        this.streams.delete(deviceToken);
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
    const adapter: SubscriptionSink<MeasurementStreamResult> = {
      next: (data) => this.fanout(deviceToken, data.measurementStream),
      error: (err) => {
        // The upstream is dead. Evict it (and drop the socket-level subscription)
        // so the NEXT subscriber for this device opens a fresh stream instead of
        // attaching to this corpse and freezing silently — the reconnect path.
        // Guard the delete so a stream that has already been replaced is left be.
        if (this.streams.get(deviceToken) === stream) this.streams.delete(deviceToken);
        stream.unsubscribe();
        for (const subscriber of stream.subscribers) subscriber.sink.error?.(err);
      },
    };
    const variables: MeasurementStreamVariables = { deviceToken, name: null };
    stream.unsubscribe = subscribe(EVENT_AREA, MEASUREMENT_STREAM, variables, adapter);

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
