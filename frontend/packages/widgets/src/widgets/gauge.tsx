// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// gauge — the current value of one measurement on an ECharts radial gauge.

import { useMemo } from 'react';

import {
  defineWidget,
  flashBackgroundStyle,
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
import { buildGaugeOption, EChart, useChartTheme } from '@devicechain/widget-sdk/echarts';

import { builtinWidgetDescriptors } from '../builtin-descriptors';

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
    () => buildGaugeOption(sample?.value ?? null, theme, { min, max, unit, precision, thresholds, scale }, size),
    [sample?.value, theme, min, max, unit, precision, thresholds, scale, size],
  );

  // Opt-in directional flash. The value lives on an opaque canvas, so the cue is a
  // translucent tint on the container behind it rather than a recolored number. The identity
  // (device + measurement) suppresses a false flash on an anchor's cross-device interleave.
  const flashDirection = useFlashOnChange(
    sample?.value,
    optBoolean(widget.options, 'flashOnChange'),
    sample && `${sample.deviceToken}:${sample.name}`,
  );

  return (
    <div style={{ width: '100%', height: '100%', ...flashBackgroundStyle(flashDirection) }}>
      <EChart option={option} />
    </div>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const gaugeWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['gauge'],
  implementation: { kind: 'trusted-react', component: Gauge },
});
