// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  WidgetFrame,
  defineWidget,
  optNumber,
  optString,
  pickSample,
  primaryMeasurementName,
} from '@devicechain/widget-sdk';
import { EChart, buildGaugeOption, useChartTheme } from '@devicechain/widget-sdk/echarts';

// The chart variant. The chart helpers live on the SDK's `/echarts` entry, so only a package
// that uses them needs `echarts` (an optional peer of the SDK).
export const tankLevelGaugeWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: {
      title: 'Tank level',
      description: 'The latest level of a tank as a gauge.',
      category: 'Telemetry',
    },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1, default: 1 } },
    options: {
      measurement: { kind: 'string', doc: 'The measurement to show.', label: 'Measurement' },
      unit: { kind: 'string', doc: 'Unit shown beside the level.', label: 'Unit' },
      capacity: {
        kind: 'number',
        min: 1,
        doc: 'The value at the top of the gauge.',
        label: 'Capacity',
        help: 'The gauge runs from zero to this value.',
      },
      window: { kind: 'number', integer: true, min: 1, doc: 'Samples the widget keeps.', label: 'Samples kept' },
    },
    defaults: { unit: '%', capacity: 100, window: 1 },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: function TankLevelGauge({ widget, data }) {
      const theme = useChartTheme();
      const sample = pickSample(data.latest, primaryMeasurementName(widget));
      const option = buildGaugeOption(sample?.value ?? null, theme, {
        min: 0,
        max: optNumber(widget.options, 'capacity') ?? 100,
        unit: optString(widget.options, 'unit'),
      });
      return (
        <WidgetFrame title="Tank level" bodyStyle={{ padding: 0 }}>
          <EChart option={option} />
        </WidgetFrame>
      );
    },
  },
});
