// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// timeseries-chart — a live line chart of the rolling measurement window, one
// series per measurement name. The window is seeded from bucketedMeasurements
// history (via useMeasurementStream's initialSamples) and extended by the live tail.

import { useMemo } from 'react';

import { buildLineOption } from '../chart-options';
import { EChart } from '../echart';
import { WidgetFrame } from '../frame';
import { useChartTheme, useElementSize } from '../hooks';
import { optString, type WidgetProps } from '../widget';

export function TimeSeriesChart({ widget, data }: WidgetProps) {
  const theme = useChartTheme();
  const [sizeRef, size] = useElementSize<HTMLDivElement>();

  const measurements = widget.datasource?.measurements;
  const option = useMemo(
    () => buildLineOption(data.samples, theme, { measurements }, size),
    [data.samples, theme, measurements, size.width, size.height],
  );

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div ref={sizeRef} style={{ width: '100%', height: '100%' }}>
        <EChart option={option} />
      </div>
    </WidgetFrame>
  );
}
