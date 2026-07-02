// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardHub — the dashboard runtime (ADR-039).
//
// It multiplexes every widget's live telemetry over the SDK's single per-area
// graphql-ws connection: many widgets bound to the same device share ONE upstream
// measurementStream subscription (ref-counted), so a crowded dashboard opens one
// stream per distinct device, not one per widget — the per-widget-subscription
// fan-out ThingsBoard collapses under (#3454). The Hub owns the subscription
// lifecycle; widgets just hand it a datasource selector and a sink.

import { subscribe, type Area, type SubscriptionSink } from '@devicechain/client';

import {
  MEASUREMENT_STREAM,
  type MeasurementStreamResult,
  type MeasurementStreamVariables,
} from './internal/measurement-doc';
import type { AnchorTarget, DatasourceSelector, MeasurementSample } from './types';

const EVENT_AREA: Area = 'event-management';

// DeviceResolver turns the token/graph references in a dashboard definition into
// the numeric device ids event-management filters on (measurementStream(deviceId:)
// matches SourceDeviceId, not the token). It is injected so this package carries
// no device-management coupling and stays unit-testable; a host backs it with
// device-management queries.
export interface DeviceResolver {
  // The numeric device id for a device token, or null if the token is unknown.
  deviceIdForToken(token: string): Promise<string | null>;
  // The numeric device ids currently anchored to the given target. This is where
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
}

// One widget's interest in a device stream: the measurement names it wants (an
// empty set means every measurement) and where to deliver them.
interface Subscriber {
  names: Set<string>;
  sink: WidgetStreamSink;
}

// The shared upstream for one distinct device id.
interface DeviceStream {
  subscribers: Set<Subscriber>;
  unsubscribe: () => void;
}

export class DashboardHub {
  private readonly resolver: DeviceResolver;
  // One entry per distinct numeric device id that has at least one subscriber.
  private readonly streams = new Map<string, DeviceStream>();

  constructor(config: DashboardHubConfig) {
    this.resolver = config.resolver;
  }

  // subscribeWidget binds a widget's datasource to a sink and returns a disposer.
  // Selector resolution is async (token→id, anchor→devices); the disposer is
  // returned synchronously and cancels a still-pending resolution, so tearing a
  // widget down before its streams open never attaches a leaked subscriber.
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
          detachers.push(this.attach(group.deviceId, group.names, sink));
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
  ): Promise<Array<{ deviceId: string; names: Set<string> }>> {
    switch (datasource.kind) {
      case 'device': {
        const id = await this.resolver.deviceIdForToken(datasource.deviceToken);
        // An unknown token is a misconfigured widget, not an empty result — throw
        // so the sink hears an error and the widget can show "device not found"
        // instead of a blank pane that never fills. (An anchor resolving to zero
        // devices, below, IS a valid empty state and stays silent.)
        if (id == null) {
          throw new Error(
            `dashboard device token '${datasource.deviceToken}' did not resolve to a device`,
          );
        }
        return [{ deviceId: id, names: new Set(datasource.measurements) }];
      }
      case 'anchor': {
        const ids = await this.resolver.devicesForAnchor(datasource.anchor);
        return ids.map((deviceId) => ({
          deviceId,
          names: new Set(datasource.measurements),
        }));
      }
      default:
        throw new Error(
          `dashboard selector kind '${datasource.kind}' is not supported yet`,
        );
    }
  }

  // attach registers a subscriber on a device's stream (opening the upstream on
  // the first subscriber) and returns a detacher that drops it and closes the
  // upstream once the last subscriber leaves.
  private attach(deviceId: string, names: Set<string>, sink: WidgetStreamSink): () => void {
    const stream = this.ensureStream(deviceId);
    const subscriber: Subscriber = { names, sink };
    stream.subscribers.add(subscriber);

    return () => {
      if (!stream.subscribers.delete(subscriber)) return;
      if (stream.subscribers.size === 0) {
        stream.unsubscribe();
        this.streams.delete(deviceId);
      }
    };
  }

  private ensureStream(deviceId: string): DeviceStream {
    const existing = this.streams.get(deviceId);
    if (existing) return existing;

    const stream: DeviceStream = { subscribers: new Set(), unsubscribe: () => {} };
    // Register before subscribing so that even a synchronously-delivered first
    // sample resolves through fanout (unsubscribe stays the no-op placeholder only
    // for the brief window until subscribe() returns the real disposer).
    this.streams.set(deviceId, stream);

    // Subscribe unfiltered by name (name: null) so a device's every reading rides
    // ONE upstream and each subscriber filters to the names it wants — a chart and
    // a card on the same device share the stream instead of opening two.
    const adapter: SubscriptionSink<MeasurementStreamResult> = {
      next: (data) => this.fanout(deviceId, data.measurementStream),
      error: (err) => {
        // The upstream is dead. Evict it (and drop the socket-level subscription)
        // so the NEXT subscriber for this device opens a fresh stream instead of
        // attaching to this corpse and freezing silently — the reconnect path.
        // Guard the delete so a stream that has already been replaced is left be.
        if (this.streams.get(deviceId) === stream) this.streams.delete(deviceId);
        stream.unsubscribe();
        for (const subscriber of stream.subscribers) subscriber.sink.error?.(err);
      },
    };
    const variables: MeasurementStreamVariables = { deviceId, name: null };
    stream.unsubscribe = subscribe(EVENT_AREA, MEASUREMENT_STREAM, variables, adapter);

    return stream;
  }

  private fanout(deviceId: string, sample: MeasurementSample): void {
    const stream = this.streams.get(deviceId);
    if (!stream) return;
    for (const subscriber of stream.subscribers) {
      if (subscriber.names.size === 0 || subscriber.names.has(sample.name)) {
        subscriber.sink.next(sample);
      }
    }
  }
}
