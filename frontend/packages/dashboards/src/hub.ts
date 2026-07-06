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

import { subscribe, type Area, type SubscriptionSink } from '@devicechain/client';

import {
  MEASUREMENT_STREAM,
  type MeasurementStreamResult,
  type MeasurementStreamVariables,
} from './internal/measurement-doc';
import type { AnchorTarget, DatasourceSelector, MeasurementSample, SlotBinding } from './types';

const EVENT_AREA: Area = 'event-management';

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
