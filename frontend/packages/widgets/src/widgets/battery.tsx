// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// battery — the charge of a device as a battery glyph filled to value% of its scale. The scale
// is the documented percent default (0..100) unless the options set another, so a voltage can
// be shown by configuring min and max. The fill takes the colour of the threshold band the
// value is in ("critical below 15") and keeps the neutral accent in a `normal` band.

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

import { emptyReason, useLevelReading, type LevelReading } from '../level-reading';
import { LevelReadout } from '../level-readout';
import { LevelEmptyFor } from '../no-reading';
import { readRange } from '../value-scale';

// The percent scale the battery uses when the options set none, stated in its descriptor.
export const BATTERY_DEFAULT_RANGE = { min: 0, max: 100 } as const;

// The glyph's own geometry, in its viewBox: a body, a terminal nub on the right, and the fill
// inset inside the body.
const VIEW = { w: 120, h: 60 };
const BODY = { x: 3, y: 3, w: 104, h: 54, r: 9 };
const NUB = { x: 110, y: 20, w: 8, h: 20, r: 3 };
const INSET = 7;
export const BATTERY_FILL_WIDTH = BODY.w - INSET * 2;

export function Battery({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
    range: readRange(optNumber(widget.options, 'min'), optNumber(widget.options, 'max'), BATTERY_DEFAULT_RANGE),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <BatteryBody reading={reading} scale={scale} />}
    </WidgetFrame>
  );
}

function BatteryBody({ reading: r, scale: ws }: { reading: LevelReading; scale: WidgetScale }) {
  const reason = emptyReason(r);
  if (reason !== undefined || !r.range?.ok || !r.fill) return <LevelEmptyFor reading={r} reason={reason} />;
  const range = r.range.range;
  const fill = r.fill;
  // The readout takes the lower share of the box; the glyph keeps the rest, scaled to fit it.
  const readoutHeight = Math.max(ws.px('font-md') * 1.6, ws.size.height * 0.4);
  return (
    <div
      data-threshold-level={r.band?.level}
      data-out-of-range={fill.position === 'within' ? undefined : fill.position}
      data-fill={fill.fraction}
      style={{ height: '100%', boxSizing: 'border-box', display: 'flex', flexDirection: 'column', gap: ws.px('space-1') }}
    >
      <div style={{ flex: '1 1 0', minHeight: 0 }}>
        <svg
          role="meter"
          aria-valuemin={range.min}
          aria-valuemax={range.max}
          aria-valuenow={range.min + fill.fraction * (range.max - range.min)}
          aria-valuetext={`${r.display}${r.unit ? ` ${r.unit}` : ''}`}
          viewBox={`0 0 ${VIEW.w} ${VIEW.h}`}
          preserveAspectRatio="xMidYMid meet"
          style={{ width: '100%', height: '100%', display: 'block' }}
        >
          <rect
            x={BODY.x}
            y={BODY.y}
            width={BODY.w}
            height={BODY.h}
            rx={BODY.r}
            style={{ fill: 'none', stroke: css('muted-foreground'), strokeWidth: 4 }}
          />
          <rect x={NUB.x} y={NUB.y} width={NUB.w} height={NUB.h} rx={NUB.r} style={{ fill: css('muted-foreground') }} />
          {fill.fraction > 0 ? (
            <rect
              data-fill-bar=""
              x={BODY.x + INSET}
              y={BODY.y + INSET}
              width={BATTERY_FILL_WIDTH * fill.fraction}
              height={BODY.h - INSET * 2}
              rx={3}
              style={{ fill: r.fillColor }}
            />
          ) : null}
        </svg>
      </div>
      <div style={{ flex: '0 0 auto', height: readoutHeight, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <LevelReadout
          text={r.display}
          unit={r.unit}
          color={r.levelColor}
          position={fill.position}
          size={{ width: ws.size.width, height: readoutHeight }}
          scale={ws.scale}
          strings={r.strings}
        />
      </div>
    </div>
  );
}
