// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// gauge — the current value of one measurement on an ECharts radial gauge.

import { useMemo } from 'react';

import { buildGaugeOption } from '../chart-options';
import { EChart } from '../echart';
import { WidgetFrame } from '../frame';
import { useChartTheme, useElementSize } from '../hooks';
import { optNumber, optString, pickSample, primaryMeasurementName, type WidgetProps } from '../widget';

export function Gauge({ widget, data }: WidgetProps) {
  const theme = useChartTheme();
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const min = optNumber(widget.options, 'min');
  const max = optNumber(widget.options, 'max');
  const unit = optString(widget.options, 'unit');

  const option = useMemo(
    () => buildGaugeOption(sample?.value ?? null, theme, { min, max, unit }, size),
    [sample?.value, theme, min, max, unit, size.width, size.height],
  );

  return (
    <WidgetFrame title={optString(widget.options, 'title') ?? name}>
      <div ref={sizeRef} style={{ width: '100%', height: '100%' }}>
        <EChart option={option} />
      </div>
    </WidgetFrame>
  );
}
