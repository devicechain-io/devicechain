// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// stat-card -- one statistic of one measurement over a span: its minimum, maximum, average, sum,
// the number of readings, or its latest reading. The figure is the server's exact aggregate over
// the span the board is set to (or the span this card is pinned to), and the card says which span
// beside it. The figure takes the colour of the threshold band it falls in and keeps the
// foreground in a `normal` band or below the lowest. DOM text, so the readability gate sees it.
//
// With `sparkline` set the card also draws the measurement's trend under the figure (sparkline.tsx):
// neutral, one point per bucket of the span's chart, and left out where it would crowd the figure.
//
// Nothing here is derived from averages: a maximum is the largest reading, not the largest bucket
// average. When the figure cannot be computed the card says why (aggregate-reading.ts).

import { useMemo } from 'react';


import { bucketSecondsFor, spanLabel, type AggregateStreamState } from '@devicechain/dashboards';
import {
  bandAt,
  css,
  FIT_MIN_FONT_PX,
  fontVar,
  formatValue,
  levelColor,
  optBoolean,
  optNumber,
  optString,
  optThresholds,
  spaceVar,
  useSeverityColors,
  WidgetFrame,
  WidgetFreshnessProvider,
  type WidgetFreshness,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { parseStat, statReading, type StatReading } from '../aggregate-reading';
import { aggregateStrings } from '../aggregate-strings';
import { LevelEmpty, LevelReadout } from '../level-readout';
import { Sparkline, trendPoints } from '../sparkline';

// The room the caption under the figure needs, before the density scale.
const CAPTION_ROOM_PX = 22;
// The strip a card's trend (`sparkline`) takes under the figure, before the density scale.
const SPARK_ROOM_PX = 28;
// The share of the width the figure is fitted to when the caption sits beside it.
const BESIDE_FIGURE_SHARE = 0.6;

// The latest reading is aged on screen like every other single reading ("12 sec. ago"), and with
// no threshold of its own it is never called stale -- only its age is shown.
const AGE_ONLY: WidgetFreshness = { at: undefined, staleAfterSeconds: undefined };

export function StatCard({ widget, data }: WidgetProps<AggregateStreamState>) {
  const severity = useSeverityColors();
  const strings = aggregateStrings();
  const stat = parseStat(optString(widget.options, 'stat'));
  const reading = statReading(widget, data, stat);
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const title = optString(widget.options, 'title') ?? reading.name;
  const wantsTrend = optBoolean(widget.options, 'sparkline');

  const card = (
    <WidgetFrame title={title} sampleTime={stat === 'last' ? (reading.kind === 'value' ? reading.time ?? null : null) : undefined}>
      {({ size, scale }) => {
        if (reading.kind === 'empty') {
          return <LevelEmpty message={emptyMessage(reading, strings)} />;
        }
        // A count is a number of readings: it has no unit, and no decimals.
        const isCount = reading.stat === 'count';
        const band = bandAt(thresholds, reading.value);
        const room = Math.round(CAPTION_ROOM_PX * scale);
        // The trend strip, when asked for and when it fits. It takes room from the figure only while
        // that leaves the figure two lines of the smallest readable size; otherwise it is left out
        // and the card is exactly the card without it.
        const floor2 = 2 * Math.round(FIT_MIN_FONT_PX * scale);
        const sparkHeight = Math.round(SPARK_ROOM_PX * scale);
        const points = wantsTrend && data.aggregates.status === 'ready' ? trendPoints(data.aggregates.series?.[reading.name ?? ''], reading.stat) : [];
        const showTrend = points.length > 0 && size.height - room - sparkHeight >= floor2;
        // The caption goes UNDER the figure while that leaves the figure two lines of the smallest
        // readable size; below that (the 2-row minimum with a subject line and a freshness footer,
        // at the smallest cell) it goes BESIDE it, so the declared minimum holds the whole card.
        const beside = size.height > 0 && size.height - room < 2 * Math.round(FIT_MIN_FONT_PX * scale);
        const figure = beside
          ? { width: Math.round(size.width * BESIDE_FIGURE_SHARE), height: size.height }
          : { width: size.width, height: Math.max(0, size.height - room - (showTrend ? sparkHeight : 0)) };
        return (
          <div
            data-stat={reading.stat}
            data-stat-span={reading.spanSeconds}
            data-stat-layout={beside ? 'beside' : 'under'}
            data-stat-trend={showTrend ? points.length : undefined}
            data-threshold-level={band?.level}
            title={band?.label}
            style={{
              display: 'flex',
              flexDirection: beside ? 'row' : 'column',
              alignItems: beside ? 'center' : undefined,
              justifyContent: 'safe center',
              gap: beside ? spaceVar(2) : undefined,
              height: '100%',
              overflow: 'hidden',
            }}
          >
            <div
              style={
                beside
                  ? { flex: '0 1 auto', minWidth: 0, display: 'flex', alignItems: 'center' }
                  : { flex: '1 1 0', minHeight: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }
              }
            >
              <LevelReadout
                text={formatValue(reading.value, isCount ? 0 : precision)}
                unit={isCount ? undefined : unit}
                color={levelColor(severity, band?.level)}
                size={figure}
                scale={scale}
              />
            </div>
            {showTrend && data.aggregates.status === 'ready' ? (
              <Sparkline
                points={points}
                height={sparkHeight}
                bucketSeconds={bucketSecondsFor(data.aggregates.spanSeconds)}
                label={strings.trend(strings.stats[reading.stat], spanLabel(data.aggregates.spanSeconds))}
              />
            ) : null}
            <div
              data-stat-caption=""
              style={{
                flex: beside ? '0 1 auto' : '0 0 auto',
                minWidth: 0,
                marginTop: beside ? undefined : spaceVar(1),
                textAlign: 'center',
                fontSize: fontVar('sm'),
                color: css('muted-foreground'),
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              {caption(reading, strings.stats)}
            </div>
          </div>
        );
      }}
    </WidgetFrame>
  );
  return stat === 'last' ? <WidgetFreshnessProvider freshness={AGE_ONLY}>{card}</WidgetFreshnessProvider> : card;
}

// "Maximum · 1h": the statistic and the span it covers. The latest reading covers no span.
function caption(r: Extract<StatReading, { kind: 'value' }>, stats: Record<string, string>): string {
  const name = stats[r.stat];
  return r.spanSeconds === undefined ? name : `${name} · ${spanLabel(r.spanSeconds)}`;
}

function emptyMessage(r: Extract<StatReading, { kind: 'empty' }>, strings: ReturnType<typeof aggregateStrings>): string {
  switch (r.reason) {
    case 'loading':
      return strings.loading;
    case 'unsupported':
      return strings.unsupported;
    case 'failed':
      return strings.failed(r.message);
    case 'noReadings':
      return strings.noReadings(r.spanSeconds === undefined ? '' : spanLabel(r.spanSeconds));
    case 'noData':
      return strings.noData;
    case 'unbound':
      return strings.unbound;
  }
}
