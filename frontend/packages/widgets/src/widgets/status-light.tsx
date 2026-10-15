// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// status-light — an indicator lamp for one measurement. Without `onAt` the lamp is lit while a
// value is present and takes the colour of the threshold band the value is in (neutral accent
// in a `normal` band), captioned with the band's label or the formatted value. With `onAt` it
// reads a 0/1-style signal: lit at or above `onAt`, dark below, captioned On/Off. No data is a
// hollow lamp captioned as such, never "off", which would be a claim about the device.

import { useMemo } from 'react';

import {
  css,
  fontVar,
  optNumber,
  optString,
  optThresholds,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { useLevelReading, type LevelReading } from '../level-reading';

export function StatusLight({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  const onAt = optNumber(widget.options, 'onAt');
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <LampBody reading={reading} onAt={onAt} scale={scale} />}
    </WidgetFrame>
  );
}

// What the lamp shows, decided once so the drawing and the tests agree.
export function lampState(r: LevelReading, onAt: number | undefined): { lit: boolean | undefined; caption: string } {
  if (r.value === undefined) return { lit: undefined, caption: r.strings.noData };
  const lit = onAt === undefined ? true : r.value >= onAt;
  const fallback = onAt === undefined ? `${r.display}${r.unit ? ` ${r.unit}` : ''}` : lit ? r.strings.on : r.strings.off;
  return { lit, caption: r.band?.label ?? fallback };
}

function LampBody({ reading: r, onAt, scale: ws }: { reading: LevelReading; onAt: number | undefined; scale: WidgetScale }) {
  const { lit, caption } = lampState(r, onAt);
  const captionRoom = ws.px('font-md') * 2.8;
  const smallest = ws.px('space-4');
  const measured = ws.size.width > 0 && ws.size.height > 0;
  // The caption goes UNDER the lamp while there is room for a lamp twice its smallest size and a
  // caption; below that (the 2x2 minimum with a subject line and a freshness footer, at the
  // smallest cell) it goes BESIDE it, on one line, so the minimum the descriptor declares holds
  // its whole content rather than pushing the caption out of the box.
  const beside = measured && ws.size.height - captionRoom < smallest * 2;
  const diameter = !measured
    ? smallest * 3
    : beside
      ? Math.max(Math.min(smallest, ws.size.height), Math.floor(Math.min(ws.size.height, ws.size.width * 0.4) * 0.8))
      : Math.max(smallest, Math.floor(Math.min(ws.size.width, ws.size.height - captionRoom) * 0.8));
  const color = r.fillColor;
  return (
    <div
      data-threshold-level={lit ? r.band?.level : undefined}
      data-lit={lit === undefined ? undefined : String(lit)}
      data-lamp-layout={beside ? 'beside' : 'under'}
      style={{
        height: '100%',
        boxSizing: 'border-box',
        overflow: 'hidden',
        display: 'flex',
        flexDirection: beside ? 'row' : 'column',
        alignItems: 'center',
        justifyContent: 'safe center',
        gap: ws.px('space-2'),
        textAlign: 'center',
      }}
    >
      <div
        data-lamp=""
        style={{
          width: diameter,
          height: diameter,
          flexShrink: 0,
          borderRadius: '50%',
          boxSizing: 'border-box',
          background: lit ? color : 'transparent',
          border: `2px solid ${lit ? color : css('muted-foreground')}`,
          boxShadow: lit ? `0 0 ${Math.round(diameter / 4)}px ${color}` : 'none',
        }}
      />
      <div
        data-lamp-caption=""
        style={{
          fontSize: fontVar('md'),
          fontWeight: 600,
          lineHeight: 1.2,
          color: lit === undefined ? css('muted-foreground') : css('foreground'),
          overflowWrap: 'anywhere',
          maxWidth: '100%',
          minWidth: 0,
        }}
      >
        {caption}
      </div>
    </div>
  );
}
