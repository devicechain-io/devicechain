// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// timeseries-chart — a live line chart of the rolling measurement window, one
// series per measurement name. Seeding the window from bucketedMeasurements
// history is a follow-up (PR D, when the app wires the query + device resolution);
// this renders the live tail from the hub stream.

import { useMemo } from 'react';

import { buildLineOption } from '../chart-options';
import { EChart } from '../echart';
import { WidgetFrame } from '../frame';
import { useChartTheme } from '../hooks';
import { optString, type WidgetProps } from '../widget';

export function TimeSeriesChart({ widget, data }: WidgetProps) {
  const theme = useChartTheme();

  const measurements = widget.datasource?.measurements;
  const option = useMemo(
    () => buildLineOption(data.samples, theme, { measurements }),
    [data.samples, theme, measurements],
  );

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <EChart option={option} />
    </WidgetFrame>
  );
}
