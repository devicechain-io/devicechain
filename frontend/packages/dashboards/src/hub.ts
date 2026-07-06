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
  ALARM_STREAM,
  ALARMS_QUERY,
  type AlarmSearchCriteriaInput,
  type AlarmStreamResult,
} from './internal/alarm-doc';
import {
  MEASUREMENT_STREAM,
  type MeasurementStreamResult,
  type MeasurementStreamVariables,
} from './internal/measurement-doc';
import type { AlarmRow, AnchorTarget, DatasourceSelector, MeasurementSample, SlotBinding } from './types';

const EVENT_AREA: Area = 'event-management';
const DEVICE_AREA: Area = 'device-management';

// Alarm channel cadence. The live stream is a best-effort trigger, so the poll is
// the correctness backstop (an alarm cleared while the socket was down still
// converges within one poll); the debounce coalesces a burst of events into one
// re-query.
const ALARM_RECONCILE_DEBOUNCE_MS = 800;
const ALARM_POLL_MS = 30_000;

// DeviceResolver turns the graph references in a dashboard definition into the
// device tokens event-management keys on (measurementStream(deviceToken:), per
// ADR-044). It is injected so this package carries no device-management coupling
// and stays unit-testable; a host backs it with device-management queries.
export interface DeviceResolver {
  // The device tokens currently anchored to the given target. This is where
  // "the Hub expands an anchor to its current membership" lives (Phase 1);
  // server-side expansion is a Phase-2 optimization.
  devicesForAnchor(anchor: AnchorTarget): Promise<string[]>;
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

export interface DashboardHubConfig {
  resolver: DeviceResolver;
  // The effective slot→entity manifest (slot defaults merged with any host override;
  // see effectiveBindings). A widget's `slot` selector resolves through this. Absent
  // slots render as an empty placeholder. Can be replaced later via setBindings.
  bindings?: Record<string, SlotBinding>;
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

export class DashboardHub implements WidgetDataSource {
  private readonly resolver: DeviceResolver;
  // One entry per distinct device token that has at least one subscriber.
  private readonly streams = new Map<string, DeviceStream>();
  // slot name → concrete entity binding. Consulted when a widget's selector is a
  // `slot`. Mutable so the authoring host can rebind live (setBindings).
  private bindings: Record<string, SlotBinding>;

  constructor(config: DashboardHubConfig) {
    this.resolver = config.resolver;
    this.bindings = config.bindings ?? {};
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
    // Monotonic generation: only the newest reconcile's result may be emitted, so a
    // slow query that resolves after a newer one can't overwrite fresher rows.
    let generation = 0;

    const dispose = (): void => {
      disposed = true;
      if (debounce) clearTimeout(debounce);
      if (poll) clearInterval(poll);
      unsubscribe?.();
    };

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
        // all is tenant-wide. Nothing to stream/poll; a later rebind builds a new hub.
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

  // disposeAll tears down every upstream stream (e.g. on dashboard close).
  disposeAll(): void {
    for (const stream of this.streams.values()) stream.unsubscribe();
    this.streams.clear();
  }

  // The number of distinct upstream device streams currently open (observability
  // + test hook — proves multiplexing collapses shared devices to one stream).
  get openStreamCount(): number {
    return this.streams.size;
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
