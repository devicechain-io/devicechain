// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// timeseries-chart — a live line chart of the rolling measurement window, one
// series per measurement name. The window is seeded from bucketedMeasurements
// history (via useMeasurementStream's initialSamples) and extended by the live tail.

import { useMemo } from 'react';

import {
  defineWidget,
  optNumber,
  optString,
  optThresholds,
  useElementSize,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';
import { buildLineOption, EChart, useChartTheme } from '@devicechain/widget-sdk/echarts';

import { builtinWidgetDescriptors } from '../builtin-descriptors';

export function TimeSeriesChart({ widget, data }: WidgetProps) {
  const theme = useChartTheme();
  const [sizeRef, size] = useElementSize<HTMLDivElement>();

  const measurements = widget.datasource?.measurements;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const option = useMemo(
    () => buildLineOption(data.samples, theme, { measurements, unit, precision, thresholds }, size),
    [data.samples, theme, measurements, unit, precision, thresholds, size.width, size.height],
  );

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div ref={sizeRef} style={{ width: '100%', height: '100%' }}>
        <EChart option={option} />
      </div>
    </WidgetFrame>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const timeSeriesChartWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['timeseries-chart'],
  implementation: { kind: 'trusted-react', component: TimeSeriesChart },
});
