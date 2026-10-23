// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// compass — a heading (wind direction, bearing) as a needle on a full turn. North is at the top
// and the angle runs clockwise, so 90 is east. A reading outside 0..360 folds onto the dial
// (370 is 10, -90 is 270). The dial is the registered gauge series; the degrees and the nearest
// of the eight points are DOM text beneath it, so the readability gate can measure them.

import { useMemo } from 'react';

import {
  optNumber,
  optString,
  optThresholds,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';
import { buildCompassOption, EChart, useChartTheme } from '@devicechain/widget-sdk/echarts';

import { cardinalOf, shownHeading } from '../heading';
import { emptyReason, useLevelReading, type LevelReading } from '../level-reading';
import { LevelReadout } from '../level-readout';
import { LevelEmptyFor } from '../no-reading';

// Whole degrees unless the options ask for more: a bearing is rarely good to a tenth.
const DEFAULT_PRECISION = 0;
// The share of the box the readout strip takes; the dial keeps the rest.
const READOUT_SHARE = 0.22;

export function Compass({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const precision = optNumber(widget.options, 'precision') ?? DEFAULT_PRECISION;
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: undefined,
    precision,
    normalize: (raw) => shownHeading(raw, precision),
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null} flush>
      {(scale) => <CompassBody reading={reading} title={title} scale={scale} />}
    </WidgetFrame>
  );
}

function CompassBody({ reading: r, title, scale: ws }: { reading: LevelReading; title: string | undefined; scale: WidgetScale }) {
  const theme = useChartTheme();
  const option = useMemo(
    () => buildCompassOption(r.value ?? null, theme, { cardinals: r.strings.cardinals, thresholds: r.thresholds, scale: ws.scale }, ws.size),
    [r.value, theme, r.strings, r.thresholds, ws.scale, ws.size],
  );
  const reason = emptyReason(r);
  if (reason !== undefined || r.value === undefined) return <LevelEmptyFor reading={r} reason={reason} />;
  const point = cardinalOf(r.value, r.strings.cardinals);
  const stripHeight = Math.max(ws.px('font-md') * 1.6, ws.size.height * READOUT_SHARE);
  return (
    <div
      role="img"
      aria-label={`${title ? `${title}, ` : ''}${r.display}° ${point}`}
      data-threshold-level={r.band?.level}
      style={{ height: '100%', display: 'flex', flexDirection: 'column' }}
    >
      <div style={{ flex: '1 1 0', minHeight: 0 }}>
        <EChart option={option} />
      </div>
      <div style={{ height: stripHeight, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
        <LevelReadout
          text={`${r.display}°`}
          unit={point}
          color={r.levelColor}
          size={{ width: ws.size.width, height: stripHeight }}
          scale={ws.scale}
          strings={r.strings}
        />
      </div>
    </div>
  );
}
