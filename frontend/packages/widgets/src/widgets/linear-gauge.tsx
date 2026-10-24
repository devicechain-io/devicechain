// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// linear-gauge — the current value of one measurement on a straight scale: a horizontal or
// vertical bar, or a thermometer (a vertical tube with a bulb). Threshold bands sit on the
// track; the fill and the readout take the colour of the band the value is in, and keep the
// widget's own neutral in a `normal` band. All DOM, so the readability gate measures its text.

import { useMemo } from 'react';

import {
  css,
  fontVar,
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

export function LinearGauge({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
    range: readRange(optNumber(widget.options, 'min'), optNumber(widget.options, 'max')),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  const thermometer = optString(widget.options, 'style') === 'thermometer';
  // A thermometer is always vertical; `orientation` applies to the bar.
  const vertical = thermometer || optString(widget.options, 'orientation') === 'vertical';
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <LinearBody reading={reading} thermometer={thermometer} vertical={vertical} scale={scale} />}
    </WidgetFrame>
  );
}

interface BodyProps {
  reading: LevelReading;
  thermometer: boolean;
  vertical: boolean;
  scale: WidgetScale;
}

function LinearBody({ reading: r, thermometer, vertical, scale: ws }: BodyProps) {
  const reason = emptyReason(r);
  if (reason !== undefined || !r.range?.ok || !r.fill) return <LevelEmptyFor reading={r} reason={reason} />;
  const range = r.range.range;
  const fill = r.fill;
  const { size, scale } = ws;
  const thickness = ws.px('space-3');
  const gap = ws.px('space-2');
  const labelPx = ws.px('font-xs');
  const minText = formatValue(range.min, r.precision);
  const maxText = formatValue(range.max, r.precision);
  const bulb = thermometer ? thickness * 2 : 0;

  // Only coloured levels draw a band: `normal`, and the stretch below the lowest threshold,
  // stay the track's own neutral.
  const bands = bandSpans(r.thresholds, range).flatMap((b) => {
    const color = r.colorOf(b.level);
    return color === undefined ? [] : [{ from: b.from, to: b.to, color }];
  });
  const track = (
    <Track
      vertical={vertical}
      thickness={thickness}
      fraction={fill.fraction}
      color={r.fillColor}
      bands={bands}
      bulb={bulb}
      aria={{
        min: range.min,
        max: range.max,
        now: range.min + fill.fraction * (range.max - range.min),
        text: `${r.display}${r.unit ? ` ${r.unit}` : ''}`,
      }}
    />
  );
  const readout = (room: { width: number; height: number }) => (
    <LevelReadout
      text={r.display}
      unit={r.unit}
      color={r.levelColor}
      position={fill.position}
      size={room}
      scale={scale}
      strings={r.strings}
    />
  );
  const labelStyle = { fontSize: fontVar('xs'), color: css('muted-foreground'), lineHeight: 1.1 } as const;
  const root = {
    height: '100%',
    boxSizing: 'border-box',
    overflow: 'hidden',
    display: 'flex',
    gap,
  } as const;
  const marks = {
    'data-threshold-level': r.band?.level,
    'data-out-of-range': fill.position === 'within' ? undefined : fill.position,
    'data-fill': fill.fraction,
  };

  if (!vertical) {
    // The readout on top, fitted to what the track and its end labels leave; the track under it.
    const trackRoom = thickness + labelPx * 1.4 + gap * 2;
    return (
      <div {...marks} style={{ ...root, flexDirection: 'column', justifyContent: 'safe center' }}>
        <div style={{ flex: '1 1 0', minHeight: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          {readout({ width: size.width, height: Math.max(0, size.height - trackRoom) })}
        </div>
        {track}
        <div style={{ display: 'flex', justifyContent: 'space-between', gap, ...labelStyle }}>
          <span>{minText}</span>
          <span>{maxText}</span>
        </div>
      </div>
    );
  }

  // Vertical: the end labels beside the tube (max on top), the readout in the room that is left.
  const labelRoom = Math.ceil(Math.max(minText.length, maxText.length) * labelPx * 0.62);
  const sideRoom = Math.max(thickness, bulb) + labelRoom + gap * 2;
  return (
    <div {...marks} style={{ ...root, flexDirection: 'row', alignItems: 'stretch' }}>
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'space-between',
          alignItems: 'flex-end',
          paddingBottom: bulb,
          ...labelStyle,
        }}
      >
        <span>{maxText}</span>
        <span>{minText}</span>
      </div>
      {track}
      <div style={{ flex: '1 1 0', minWidth: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        {readout({ width: Math.max(0, size.width - sideRoom), height: size.height })}
      </div>
    </div>
  );
}

interface TrackProps {
  vertical: boolean;
  thickness: number;
  fraction: number;
  color: string;
  bands: { from: number; to: number; color: string }[];
  bulb: number;
  aria: { min: number; max: number; now: number; text: string };
}

function Track({ vertical, thickness, fraction, color, bands, bulb, aria }: TrackProps) {
  const pct = (f: number) => `${f * 100}%`;
  const radius = thickness / 2;
  const tube = (
    <div
      role="meter"
      aria-valuemin={aria.min}
      aria-valuemax={aria.max}
      aria-valuenow={aria.now}
      aria-valuetext={aria.text}
      style={{
        position: 'relative',
        overflow: 'hidden',
        background: css('muted'),
        borderRadius: radius,
        flexShrink: 0,
        ...(vertical ? { width: thickness, flex: '1 1 0', minHeight: 0 } : { width: '100%', height: thickness }),
      }}
    >
      {bands.map((b, i) => (
        <div
          key={i}
          data-band=""
          style={{
            position: 'absolute',
            background: b.color,
            opacity: 0.35,
            ...(vertical
              ? { left: 0, right: 0, bottom: pct(b.from), height: pct(b.to - b.from) }
              : { top: 0, bottom: 0, left: pct(b.from), width: pct(b.to - b.from) }),
          }}
        />
      ))}
      <div
        data-fill-bar=""
        style={{
          position: 'absolute',
          background: color,
          ...(vertical
            ? { left: 0, right: 0, bottom: 0, height: pct(fraction) }
            : { top: 0, bottom: 0, left: 0, width: pct(fraction) }),
        }}
      />
      <div
        data-marker=""
        style={{
          position: 'absolute',
          background: css('foreground'),
          ...(vertical
            ? { left: 0, right: 0, height: 2, bottom: `calc(${pct(fraction)} - 1px)` }
            : { top: 0, bottom: 0, width: 2, left: `calc(${pct(fraction)} - 1px)` }),
        }}
      />
    </div>
  );
  if (!vertical) return tube;
  // A vertical tube sits in a column of its own so a thermometer's bulb (wider than the tube)
  // can hang under it, overlapping its foot so the two read as one glass.
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', minHeight: 0, flex: '0 0 auto', width: Math.max(thickness, bulb) }}>
      {tube}
      {bulb > 0 ? (
        <div
          data-bulb=""
          style={{ width: bulb, height: bulb, borderRadius: '50%', background: color, marginTop: -radius, flexShrink: 0 }}
        />
      ) : null}
    </div>
  );
}
