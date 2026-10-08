// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// History seeding — turns event-management's bucketedMeasurements aggregates into
// MeasurementSamples so a chart shows recent history immediately instead of
// filling in only from live data. Each bucket becomes one synthetic sample at the
// bucket start carrying its average; the live tail then extends it.
//
// Seeds `device` selectors only (single numeric id). Anchor (multi-device) history
// seeding is deferred — the live stream still populates anchor charts.

import { gql } from '@devicechain/client';

import { BUCKETED_MEASUREMENTS } from './queries';
import type { MeasurementSample, WidgetInstance } from './types';

export interface HistoryWindow {
  startTime: string;
  endTime: string;
  intervalSeconds: number;
}

// The default backfill: the last hour bucketed per minute (~60 points) — enough to
// give a chart shape on load without a heavy query.
export function defaultHistoryWindow(): HistoryWindow {
  const now = Date.now();
  return {
    startTime: new Date(now - 60 * 60 * 1000).toISOString(),
    endTime: new Date(now).toISOString(),
    intervalSeconds: 60,
  };
}

// fetchWidgetHistory returns seed samples for one widget, or [] when it has no
// device datasource (label/image, an anchor selector, an unbound slot). Never rejects
// into the render path — a failed backfill just yields an empty seed and the live stream
// still fills the widget.
//
// It takes the widget with its CONCRETE datasource — a slot already resolved through the
// bindings (resolveWidgetDatasource), as the renderer passes it — so the seed is for
// exactly the device the widget's live stream is for. A raw slot seeds nothing.
export async function fetchWidgetHistory(
  widget: WidgetInstance,
  window: HistoryWindow,
): Promise<MeasurementSample[]> {
  const ds = widget.datasource;
  if (!ds || ds.kind !== 'device') return [];

  try {
    // measurementStream and bucketedMeasurements are keyed by device token (ADR-044),
    // so the token goes straight into the criteria — no token→id hop.
    const deviceToken = ds.deviceToken;

    // Seed each requested measurement (or all, when the widget lists none).
    const names: Array<string | undefined> = ds.measurements.length ? ds.measurements : [undefined];
    const pages = await Promise.all(
      names.map((name) =>
        gql('event-management', BUCKETED_MEASUREMENTS, {
          criteria: {
            deviceToken,
            name,
            startTime: window.startTime,
            endTime: window.endTime,
            intervalSeconds: window.intervalSeconds,
          },
        }).then((r) => r.bucketedMeasurements),
      ),
    );

    return pages
      .flat()
      .filter((b) => b.avg != null)
      .map((b) => ({
        id: `${deviceToken}-${b.name}-${b.bucketStart}`,
        deviceToken,
        eventType: 0,
        occurredTime: b.bucketStart,
        name: b.name,
        value: b.avg,
        classifier: null,
      }))
      .sort((a, b) => (a.occurredTime! < b.occurredTime! ? -1 : 1));
  } catch {
    return [];
  }
}
