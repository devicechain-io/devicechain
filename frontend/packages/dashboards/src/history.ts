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
import { DEFAULT_TIME_RANGE_SECONDS, bucketSecondsFor, rangeWindow } from './time-range';
import type { MeasurementSample, WidgetInstance } from './types';

export interface HistoryWindow {
  startTime: string;
  endTime: string;
  intervalSeconds: number;
}

// historyWindowFor is the seed window for a span: `spanSeconds` back from `end` (epoch ms),
// bucketed at the width bucketSecondsFor picks so the read stays within MAX_SEED_BUCKETS per
// measurement whatever the span (7 days is 240 buckets, not 10 080 minutes). `end` is the
// moment the board is frozen at while paused, otherwise now.
export function historyWindowFor(spanSeconds: number, end: number): HistoryWindow {
  const { startTime, endTime } = rangeWindow({ spanSeconds, paused: false }, end);
  return { startTime, endTime, intervalSeconds: bucketSecondsFor(spanSeconds) };
}

// The default backfill: the last hour bucketed per minute (~60 points), enough to
// give a chart shape on load without a heavy query. What a widget that is not time-ranged
// (a card, a gauge: they need the latest value, not a span) is seeded with.
export function defaultHistoryWindow(): HistoryWindow {
  return historyWindowFor(DEFAULT_TIME_RANGE_SECONDS, Date.now());
}

// fetchWidgetHistory returns seed samples for one widget, or [] when it has no
// device datasource (label/image, an anchor selector, an unbound slot). Never rejects
// into the render path — a failed backfill just yields an empty seed and the live stream
// still fills the widget.
//
// It takes the widget with its CONCRETE datasource — a slot already resolved through the
// bindings (resolveWidgetDatasource), as the renderer passes it — so the seed is for
// exactly the device the widget's live stream is for. A raw slot seeds nothing.
//
// `rethrow` makes a failed read reject instead. A caller REPLACING a seed it already shows (the
// re-read after a live feed was interrupted) must not mistake a failure for "no history": an empty
// answer would wipe the history on screen and claim the outage held no readings.
export async function fetchWidgetHistory(
  widget: WidgetInstance,
  window: HistoryWindow,
  options: { rethrow?: boolean } = {},
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
        bucketSeconds: window.intervalSeconds,
      }))
      .sort((a, b) => (a.occurredTime! < b.occurredTime! ? -1 : 1));
  } catch (err) {
    if (options.rethrow) throw err;
    return [];
  }
}
