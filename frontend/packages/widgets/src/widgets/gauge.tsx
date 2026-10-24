// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// gauge — the current value of one measurement on an ECharts radial gauge.

import { useMemo } from 'react';

import {
  defineWidget,
  flashBackgroundStyle,
  formatValue,
  optBoolean,
  optNumber,
  optString,
  optThresholds,
  pickSample,
  primaryMeasurementName,
  useFlashOnChange,
  useWidgetScale,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';
import { buildGaugeOption, EChart, gaugeReadoutBox, loadChartEngine, useChartTheme } from '@devicechain/widget-sdk/echarts';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { NoReading, useSettledEmpty } from './no-reading-lazy';
import { LevelReadout } from '../level-readout';
import { readRange } from '../value-scale';

export function Gauge({ widget, data }: WidgetProps) {
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);
  return (
    <WidgetFrame title={optString(widget.options, 'title') ?? name} sampleTime={sample?.occurredTime ?? null} flush>
      <GaugeBody widget={widget} data={data} />
    </WidgetFrame>
  );
}

// Rendered INSIDE the frame: the widget scale is the frame's content column, which is only in
// context below the frame.
function GaugeBody({ widget, data }: WidgetProps) {
  const theme = useChartTheme();
  const { size, scale } = useWidgetScale();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const min = optNumber(widget.options, 'min');
  const max = optNumber(widget.options, 'max');
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  // The stored list is re-parsed only when the options bag changes, so the memo below keys on
  // a stable array rather than a fresh one per render.
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);

  const option = useMemo(
    () => buildGaugeOption(sample?.value ?? null, theme, { min, max, thresholds, scale, readout: 'none', emptyNeedle: 'none' }, size),
    [sample?.value, theme, min, max, thresholds, scale, size],
  );

  // Opt-in directional flash. The value lives on an opaque canvas, so the cue is a
  // translucent tint on the container behind it rather than a recolored number. The identity
  // (device + measurement) suppresses a false flash on an anchor's cross-device interleave.
  const flashDirection = useFlashOnChange(
    sample?.value,
    optBoolean(widget.options, 'flashOnChange'),
    sample && `${sample.deviceToken}:${sample.name}`,
  );

  // The number is DOM text laid over the dial where the canvas used to print it, so the
  // readability gate can measure it (it cannot see canvas text). The meter role carries the
  // reading to assistive tech, which the canvas never did.
  const box = gaugeReadoutBox(size);
  const range = readRange(min, max, { min: 0, max: 100 });
  const value = sample?.value ?? undefined;
  const meter =
    range.ok && value !== undefined && Number.isFinite(value)
      ? {
          role: 'meter' as const,
          'aria-valuemin': range.range.min,
          'aria-valuemax': range.range.max,
          'aria-valuenow': Math.min(range.range.max, Math.max(range.range.min, value)),
          'aria-valuetext': `${formatValue(value, precision)}${unit ? ` ${unit}` : ''}`,
        }
      : {};

  // No reading: the dial would be an empty arc; say why instead (after the first moment, while a
  // board is still loading the dial is drawn as it always was).
  const explain = useSettledEmpty(value === undefined);
  if (explain) {
    return <NoReading unbound={widget.datasource?.kind === 'unbound'} measurement={name} waitMs={0} />;
  }

  return (
    <div style={{ position: 'relative', width: '100%', height: '100%', ...flashBackgroundStyle(flashDirection) }} {...meter}>
      <EChart option={option} />
      <div
        style={{
          position: 'absolute',
          left: box.centerX - box.width / 2,
          top: box.centerY - box.height / 2,
          width: box.width,
          height: box.height,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          pointerEvents: 'none',
        }}
      >
        <LevelReadout
          text={formatValue(value, precision)}
          unit={value === undefined ? undefined : unit}
          size={{ width: box.width, height: box.height }}
          scale={scale}
        />
      </div>
    </div>
  );
}

// What preloadWidgets calls: the chart engine loads on demand, as its own chunk, so a host that
// awaits this for a board holding this widget draws the chart on the board's first frame.
Gauge.preload = loadChartEngine;

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const gaugeWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['gauge'],
  implementation: { kind: 'trusted-react', component: Gauge },
});
