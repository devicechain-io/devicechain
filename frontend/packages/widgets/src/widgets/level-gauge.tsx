// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// level-gauge — a tank: the current value of one measurement as how full a vessel is, between a
// configured min (empty) and max (full). The liquid takes the colour of the threshold band the
// value is in and keeps the widget's neutral accent in a `normal` band; the readout floats over
// it, as the value or as a percent of the scale. All DOM, so the readability gate sees its text.

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
import { bandSpans, readRange } from '../value-scale';

// The strength of a band the level is not in: the linear gauge's track strength.
const MUTED_BAND_OPACITY = 0.35;

export function LevelGauge({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
    range: readRange(optNumber(widget.options, 'min'), optNumber(widget.options, 'max')),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  const percent = optString(widget.options, 'readout') === 'percent';
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <TankBody reading={reading} percent={percent} scale={scale} />}
    </WidgetFrame>
  );
}

function TankBody({ reading: r, percent, scale: ws }: { reading: LevelReading; percent: boolean; scale: WidgetScale }) {
  const reason = emptyReason(r);
  if (reason !== undefined || !r.range?.ok || !r.fill) return <LevelEmptyFor reading={r} reason={reason} />;
  const range = r.range.range;
  const fill = r.fill;
  const wall = 2;
  const stripe = ws.px('space-1');
  // The percent is of the scale and is NOT clamped: 130 % of a tank is the truth the marker flags.
  const text = percent ? `${formatValue(fill.ratio * 100, r.precision ?? 0)}` : r.display;
  const unit = percent ? '%' : r.unit;
  // A band is a SCALE MARKER until the level is in it: drawn muted, as the linear gauge draws its
  // track, so a full tank's low-fuel band does not read as a warning at a glance. The band the
  // level is in is drawn at full strength, agreeing with the liquid's colour.
  const bands = bandSpans(r.thresholds, range).flatMap((b) => {
    const color = r.colorOf(b.level);
    const active = fill.fraction >= b.from && (fill.fraction < b.to || (b.to >= 1 && fill.fraction >= 1));
    return color === undefined ? [] : [{ from: b.from, to: b.to, color, active }];
  });
  const inner = { width: Math.max(0, ws.size.width - wall * 2 - stripe), height: Math.max(0, ws.size.height - wall * 2) };
  return (
    <div
      data-threshold-level={r.band?.level}
      data-out-of-range={fill.position === 'within' ? undefined : fill.position}
      data-fill={fill.fraction}
      style={{ height: '100%', boxSizing: 'border-box', display: 'flex', gap: ws.px('space-1') }}
    >
      <div
        role="meter"
        aria-valuemin={range.min}
        aria-valuemax={range.max}
        aria-valuenow={range.min + fill.fraction * (range.max - range.min)}
        aria-valuetext={`${r.display}${r.unit ? ` ${r.unit}` : ''}`}
        style={{
          position: 'relative',
          flex: '1 1 0',
          minWidth: 0,
          overflow: 'hidden',
          boxSizing: 'border-box',
          border: `${wall}px solid ${css('border')}`,
          borderRadius: ws.px('space-2'),
          background: css('muted'),
        }}
      >
        <div
          data-liquid=""
          style={{
            position: 'absolute',
            left: 0,
            right: 0,
            bottom: 0,
            height: `${fill.fraction * 100}%`,
            background: r.fillColor,
            opacity: 0.55,
            borderTop: fill.fraction > 0 && fill.fraction < 1 ? `2px solid ${r.fillColor}` : undefined,
          }}
        />
        <div style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <LevelReadout
            text={text}
            unit={unit}
            color={r.levelColor}
            position={fill.position}
            size={inner}
            scale={ws.scale}
            strings={r.strings}
          />
        </div>
      </div>
      {/* The bands as a strip beside the tank, so the thresholds read against the level. */}
      <div style={{ position: 'relative', width: stripe, flexShrink: 0, borderRadius: stripe / 2, background: css('muted'), overflow: 'hidden' }}>
        {bands.map((b, i) => (
          <div
            key={i}
            data-band=""
            data-band-active={b.active ? '' : undefined}
            style={{
              position: 'absolute',
              left: 0,
              right: 0,
              bottom: `${b.from * 100}%`,
              height: `${(b.to - b.from) * 100}%`,
              background: b.color,
              opacity: b.active ? 1 : MUTED_BAND_OPACITY,
            }}
          />
        ))}
      </div>
    </div>
  );
}
