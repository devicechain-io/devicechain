// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type {
  DashboardHub,
  DatasourceSelector,
  MeasurementSample,
  WidgetInstance,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

// No globals config, so register testing-library's DOM teardown ourselves —
// otherwise renders leak across tests and text matches find stale elements.
afterEach(cleanup);

// jsdom has no canvas, so stub the ECharts wrapper and expose the option it was
// given so the gauge test can assert the value→chart mapping.
vi.mock('./echart', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  EChart: ({ option }: { option: any }) => (
    <div data-testid="echart" data-series={JSON.stringify(option.series)} />
  ),
}));

import { ConnectedWidget } from './connected-widget';
import type { MeasurementStreamState } from './hooks';
import { Gauge } from './widgets/gauge';
import { Image } from './widgets/image';
import { Label } from './widgets/label';
import { LatestCard } from './widgets/latest-card';
import { Table } from './widgets/table';

const deviceDs: DatasourceSelector = {
  kind: 'device',
  deviceToken: 'therm-001',
  measurements: ['temperature'],
};

const widget = (
  type: WidgetInstance['type'],
  options: Record<string, unknown> = {},
  datasource: DatasourceSelector | undefined = deviceDs,
): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { x: 0, y: 0, w: 2, h: 2, z: 1 } },
  datasource,
  options,
});

const sample = (name: string, value: number, time: string): MeasurementSample => ({
  id: `${name}-${time}`,
  deviceToken: 'therm-001',
  eventType: 0,
  occurredTime: time,
  name,
  value,
  classifier: null,
});

// Build a resolved data prop from a set of latest samples.
const data = (...samples: MeasurementSample[]): MeasurementStreamState => ({
  latest: Object.fromEntries(samples.map((s) => [s.name, s])),
  samples,
  error: null,
});

describe('widgets (pure, from resolved data)', () => {
  it('LatestCard shows the latest value with precision and unit', () => {
    render(
      <LatestCard
        widget={widget('latest-card', { unit: '°C', precision: 1 })}
        data={data(sample('temperature', 20.456, 't1'))}
      />,
    );
    expect(screen.getByText('20.5')).toBeTruthy();
    expect(screen.getByText('°C')).toBeTruthy();
  });

  it('Table renders one row per measurement name', () => {
    render(
      <Table
        widget={widget('table', {}, { kind: 'device', deviceToken: 'x', measurements: ['temperature', 'humidity'] })}
        data={data(sample('temperature', 20, 't1'), sample('humidity', 55, 't2'))}
      />,
    );
    expect(screen.getByText('temperature')).toBeTruthy();
    expect(screen.getByText('humidity')).toBeTruthy();
  });

  it('Label renders static text and needs no data', () => {
    render(<Label widget={widget('label', { text: 'Zone A' }, undefined)} data={data()} />);
    expect(screen.getByText('Zone A')).toBeTruthy();
  });

  it('Image shows a placeholder without a url and an <img> with one', () => {
    const { rerender } = render(<Image widget={widget('image', {}, undefined)} data={data()} />);
    expect(screen.getByText('No image URL')).toBeTruthy();
    rerender(
      <Image widget={widget('image', { url: 'http://x/y.png', alt: 'floor plan' }, undefined)} data={data()} />,
    );
    expect(screen.getByAltText('floor plan')).toBeTruthy();
  });

  it('Gauge feeds the latest value into the chart option', () => {
    render(<Gauge widget={widget('gauge', { min: 0, max: 50 })} data={data(sample('temperature', 30, 't1'))} />);
    const series = JSON.parse(screen.getByTestId('echart').getAttribute('data-series') as string);
    expect(series[0].data[0].value).toBe(30);
    expect(series[0].max).toBe(50);
  });
});

describe('ConnectedWidget', () => {
  it('streams hub samples through the hook into the widget', () => {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_ds: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;

    render(<ConnectedWidget widget={widget('latest-card', { precision: 0 })} hub={hub} />);
    act(() => sink?.next(sample('temperature', 42, 't1')));
    expect(screen.getByText('42')).toBeTruthy();
  });

  it('renders a "Data unavailable" state when the stream errors', () => {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_ds: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;

    render(<ConnectedWidget widget={widget('latest-card', { title: 'Temp', precision: 0 })} hub={hub} />);
    act(() => sink?.error?.(new Error('device not found')));
    expect(screen.getByText('Data unavailable')).toBeTruthy();
    expect(screen.getByText(/device not found/)).toBeTruthy();
  });

  it('renders a "Device unavailable" pane when the bound device no longer exists', async () => {
    const hub = {
      subscribeWidget: (_ds: DatasourceSelector, _s: WidgetStreamSink) => () => {},
      isDatasourceAvailable: async () => false, // deleted device
    } as unknown as DashboardHub;

    render(<ConnectedWidget widget={widget('latest-card', { precision: 0 })} hub={hub} />);
    await act(async () => {}); // let the optimistic availability check resolve
    expect(screen.getByText('Device unavailable')).toBeTruthy();
  });
});
