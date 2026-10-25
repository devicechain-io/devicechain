// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// progress-ring — how far one measurement has come between a configured min and max, as a ring
// that fills clockwise from the top. The readout in the middle is the percent of the scale (or
// the value itself, by option). The arc takes the colour of the threshold band the value is in
// and keeps the neutral accent in a `normal` band. A value off the scale pins the ring to its
// end and carries a marker beside the true reading. SVG and DOM, so the gate sees the text.

import { useMemo } from 'react';

import {
  css,
  formatValue,
  optNumber,
  optString,
  optThresholds,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { emptyReason, useLevelReading, type LevelReading } from '../level-reading';
import { LevelReadout } from '../level-readout';
import { LevelEmptyFor } from '../no-reading';
import { readRange } from '../value-scale';

// The ring's geometry in its 100 x 100 viewBox.
const RING_RADIUS = 42;
const RING_STROKE = 10;
export const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;
// The readout's room inside the ring, as a share of the side.
const INNER = { width: 0.62, height: 0.34 };

export function ProgressRing({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
    range: readRange(optNumber(widget.options, 'min'), optNumber(widget.options, 'max')),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  const percent = optString(widget.options, 'readout') !== 'value';
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <RingBody reading={reading} percent={percent} scale={scale} />}
    </WidgetFrame>
  );
}

function RingBody({ reading: r, percent, scale: ws }: { reading: LevelReading; percent: boolean; scale: WidgetScale }) {
  const reason = emptyReason(r);
  if (reason !== undefined || !r.range?.ok || !r.fill) return <LevelEmptyFor reading={r} reason={reason} />;
  const range = r.range.range;
  const fill = r.fill;
  // The percent is of the scale and is NOT clamped: 150 % is the truth the marker flags.
  const text = percent ? formatValue(fill.ratio * 100, r.precision ?? 0) : r.display;
  const unit = percent ? '%' : r.unit;
  const side = Math.max(0, Math.min(ws.size.width, ws.size.height));
  return (
    <div
      role="meter"
      aria-valuemin={range.min}
      aria-valuemax={range.max}
      aria-valuenow={range.min + fill.fraction * (range.max - range.min)}
      aria-valuetext={`${r.display}${r.unit ? ` ${r.unit}` : ''}`}
      data-threshold-level={r.band?.level}
      data-out-of-range={fill.position === 'within' ? undefined : fill.position}
      data-fill={fill.fraction}
      style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}
    >
      <div style={{ position: 'relative', width: side || '100%', height: side || '100%', aspectRatio: '1' }}>
        <svg viewBox="0 0 100 100" width="100%" height="100%" aria-hidden="true" style={{ display: 'block' }}>
          <circle cx="50" cy="50" r={RING_RADIUS} fill="none" strokeWidth={RING_STROKE} style={{ stroke: css('muted') }} />
          <circle
            data-ring-fill=""
            cx="50"
            cy="50"
            r={RING_RADIUS}
            fill="none"
            strokeWidth={RING_STROKE}
            strokeDasharray={`${fill.fraction * RING_CIRCUMFERENCE} ${RING_CIRCUMFERENCE}`}
            transform="rotate(-90 50 50)"
            style={{ stroke: r.fillColor }}
          />
        </svg>
        <div style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <LevelReadout
            text={text}
            unit={unit}
            color={r.levelColor}
            position={fill.position}
            size={{ width: side * INNER.width, height: side * INNER.height }}
            scale={ws.scale}
            strings={r.strings}
          />
        </div>
      </div>
    </div>
  );
}
