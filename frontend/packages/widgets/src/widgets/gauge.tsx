// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// gauge — the current value of one measurement on an ECharts radial gauge.

import { useMemo } from 'react';

import { buildGaugeOption } from '../chart-options';
import { EChart } from '../echart';
import { WidgetFrame } from '../frame';
import { useChartTheme } from '../hooks';
import { optNumber, optString, pickSample, primaryMeasurementName, type WidgetProps } from '../widget';

export function Gauge({ widget, data }: WidgetProps) {
  const theme = useChartTheme();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const min = optNumber(widget.options, 'min');
  const max = optNumber(widget.options, 'max');
  const unit = optString(widget.options, 'unit');

  const option = useMemo(
    () => buildGaugeOption(sample?.value ?? null, theme, { min, max, unit }),
    [sample?.value, theme, min, max, unit],
  );

  return (
    <WidgetFrame title={optString(widget.options, 'title') ?? name}>
      <EChart option={option} />
    </WidgetFrame>
  );
}
