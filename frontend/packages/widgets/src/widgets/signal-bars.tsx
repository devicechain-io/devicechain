// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// signal-bars — a signal-strength glyph. The author's thresholds ARE the bars: one bar per
// threshold, lit for each the value has reached, in the colour of the band the value is in
// (neutral accent in a `normal` band). Nothing is assumed about the unit or the scale, so a dBm
// board and a 0-31 CSQ board configure their own steps; with fewer than two thresholds there is
// nothing to draw and the widget says so.

import { useMemo } from 'react';

import {
  css,
  optNumber,
  optString,
  optThresholds,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { useLevelReading, type LevelReading } from '../level-reading';
import { LevelEmpty, LevelReadout } from '../level-readout';
import { LevelEmptyFor } from '../no-reading';
import { litBars } from '../value-scale';

export const SIGNAL_MIN_BARS = 2;

const VIEW = { w: 100, h: 60 };

export function SignalBars({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <BarsBody reading={reading} scale={scale} />}
    </WidgetFrame>
  );
}

function BarsBody({ reading: r, scale: ws }: { reading: LevelReading; scale: WidgetScale }) {
  if (r.thresholds.length < SIGNAL_MIN_BARS) return <LevelEmpty message={r.strings.setThresholds} />;
  const bars = litBars(r.thresholds, r.value);
  if (!bars) return <LevelEmptyFor reading={r} reason={undefined} />;
  const n = bars.total;
  // n bars and n-1 gaps of a third of a bar's width across the viewBox.
  const barW = VIEW.w / (n + (n - 1) / 3);
  const gapW = barW / 3;
  const readoutHeight = Math.max(ws.px('font-md') * 1.6, ws.size.height * 0.34);
  return (
    <div
      data-threshold-level={r.band?.level}
      data-bars-lit={bars.lit}
      data-bars-total={bars.total}
      style={{ height: '100%', boxSizing: 'border-box', display: 'flex', flexDirection: 'column', gap: ws.px('space-1') }}
    >
      <div style={{ flex: '1 1 0', minHeight: 0 }}>
        <svg
          role="meter"
          aria-valuemin={0}
          aria-valuemax={bars.total}
          aria-valuenow={bars.lit}
          aria-valuetext={`${bars.lit}/${bars.total}`}
          viewBox={`0 0 ${VIEW.w} ${VIEW.h}`}
          preserveAspectRatio="xMidYMax meet"
          style={{ width: '100%', height: '100%', display: 'block' }}
        >
          {Array.from({ length: n }, (_, i) => {
            const h = (VIEW.h * (i + 1)) / n;
            const lit = i < bars.lit;
            return (
              <rect
                key={i}
                data-bar={lit ? 'lit' : 'unlit'}
                x={i * (barW + gapW)}
                y={VIEW.h - h}
                width={barW}
                height={h}
                rx={Math.min(3, barW / 3)}
                style={{ fill: lit ? r.fillColor : css('muted') }}
              />
            );
          })}
        </svg>
      </div>
      <div style={{ flex: '0 0 auto', height: readoutHeight, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <LevelReadout
          text={r.display}
          unit={r.unit}
          color={r.levelColor}
          size={{ width: ws.size.width, height: readoutHeight }}
          scale={ws.scale}
          strings={r.strings}
        />
      </div>
    </div>
  );
}
