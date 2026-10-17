// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type {
  DashboardHub,
  DatasourceSelector,
  MeasurementSample,
  WidgetInstance,
  WidgetType,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// No globals config, so register testing-library's DOM teardown ourselves —
// otherwise renders leak across tests and text matches find stale elements.
afterEach(cleanup);

// jsdom has no canvas, so stub the ECharts wrapper and expose the option it was
// given so the gauge test can assert the value→chart mapping.
vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  // Only the canvas wrapper is replaced (jsdom has no canvas); the pure option builders and
  // the colour theme stay real so a chart widget still builds the option it would draw.
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    EChart: ({ option }: { option: any }) => (
      <div data-testid="echart" data-series={JSON.stringify(option.series)} />
    ),
  };
});

import { GraphQLRequestError } from '@devicechain/client';
import { ConnectedWidget } from './connected-widget';
import { type MeasurementStreamState } from '@devicechain/widget-sdk';
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
  type: WidgetType,
  options: Record<string, unknown> = {},
  datasource: DatasourceSelector | undefined = deviceDs,
): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 2, rows: 2 } },
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
    render(<Label widget={widget('label', { text: 'Zone A' }, undefined)} data={null} />);
    expect(screen.getByText('Zone A')).toBeTruthy();
  });

  it('Image shows a placeholder without a url and an <img> with one', () => {
    const { rerender } = render(<Image widget={widget('image', {}, undefined)} data={null} />);
    expect(screen.getByText('No image URL')).toBeTruthy();
    rerender(
      <Image widget={widget('image', { url: 'http://x/y.png', alt: 'floor plan' }, undefined)} data={null} />,
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

  // A refusal is a state, not an outage: the plain sentence the fleet widgets show, never
  // "Data unavailable" over the server's raw "forbidden: missing required authority".
  it('renders a refused read as no access rather than as "Data unavailable"', () => {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_ds: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;

    render(<ConnectedWidget widget={widget('latest-card', { title: 'Temp', precision: 0 })} hub={hub} />);
    act(() =>
      sink?.error?.(
        new GraphQLRequestError('GraphQL error', 200, [
          { message: 'forbidden: missing required authority', extensions: { code: 'FORBIDDEN' } },
        ]),
      ),
    );
    expect(screen.getByText('You do not have access to this data')).toBeTruthy();
    expect(screen.queryByText('Data unavailable')).toBeNull();
    expect(screen.queryByText(/missing required authority/)).toBeNull();
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

  // A customer user viewing a widget bound to an area or an asset: the platform does not offer
  // that binding to them, and the widget says so in words rather than rendering empty.
  describe('a binding not offered to customer users', () => {
    const areaDs: DatasourceSelector = {
      kind: 'anchor',
      anchor: { relationship: 'located-in', targetType: 'area', targetToken: 'plant' },
      measurements: ['temperature'],
    };
    const hubSupporting = (supported: boolean | undefined) => {
      const asked: DatasourceSelector[] = [];
      const hub = {
        subscribeWidget: () => () => {},
        subscribeMembers: () => () => {},
        readMemberSeeds: async () => {},
        isDatasourceAvailable: async () => true,
        ...(supported === undefined
          ? {}
          : {
              isDatasourceSupported: async (ds: DatasourceSelector) => {
                asked.push(ds);
                return supported;
              },
            }),
      } as unknown as DashboardHub;
      return { hub, asked };
    };

    it('shows "Not available for customer users" when the source refuses the binding', async () => {
      const { hub, asked } = hubSupporting(false);
      render(<ConnectedWidget widget={widget('latest-card', { title: 'Plant' }, areaDs)} hub={hub} />);
      await act(async () => {});
      expect(screen.getByText('Not available for customer users')).toBeTruthy();
      expect(screen.getByText('Plant')).toBeTruthy();
      expect(asked).toEqual([areaDs]);
    });

    it('shows nothing of the kind when the source supports it, or cannot say', async () => {
      for (const supported of [true, undefined]) {
        const { hub } = hubSupporting(supported);
        render(<ConnectedWidget widget={widget('latest-card', {}, areaDs)} hub={hub} />);
        await act(async () => {});
        expect(screen.queryByText('Not available for customer users')).toBeNull();
        cleanup();
      }
    });

    it('does not ask about a widget that reads no datasource', async () => {
      const { hub, asked } = hubSupporting(false);
      render(<ConnectedWidget widget={widget('label', { text: 'hello' }, areaDs)} hub={hub} />);
      await act(async () => {});
      expect(asked).toEqual([]);
      expect(screen.queryByText('Not available for customer users')).toBeNull();
    });
  });
});

describe('ConnectedWidget freshness (descriptor opt-in)', () => {
  const NOW = Date.parse('2026-10-14T12:00:00Z');
  const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();

  function hubWith(ago: number) {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_ds: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    return { hub, push: () => act(() => sink?.next(sample('temperature', 42, at(ago)))) };
  }

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    document.documentElement.lang = 'en';
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('shows the sample age on an opted-in widget and ticks it', () => {
    const { hub, push } = hubWith(12);
    render(<ConnectedWidget widget={widget('latest-card', { precision: 0 })} hub={hub} />);
    push();
    expect(screen.getByText(/12 sec/)).toBeTruthy();
    act(() => void vi.advanceTimersByTime(3000));
    expect(screen.getByText(/15 sec/)).toBeTruthy();
  });

  it('marks it stale only when the widget sets staleAfterSeconds and the sample is older', () => {
    const { hub, push } = hubWith(120);
    const { unmount } = render(
      <ConnectedWidget widget={widget('latest-card', { precision: 0, staleAfterSeconds: 60 })} hub={hub} />,
    );
    push();
    expect(screen.getByText('stale')).toBeTruthy();
    unmount();

    const quiet = hubWith(120);
    render(<ConnectedWidget widget={widget('latest-card', { precision: 0 })} hub={quiet.hub} />);
    quiet.push();
    expect(screen.getByText(/2 min/)).toBeTruthy();
    expect(screen.queryByText('stale')).toBeNull();
  });

  it('ages the value a single-value widget SHOWS, not a fresher sibling measurement', () => {
    // The card shows temperature (its datasource's first measurement); humidity arrived just
    // now. Aging the newest sample would make the frozen temperature look live.
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_ds: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    const ds: DatasourceSelector = { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature', 'humidity'] };
    render(<ConnectedWidget widget={widget('latest-card', { precision: 0, staleAfterSeconds: 60 }, ds)} hub={hub} />);
    act(() => {
      sink?.next(sample('temperature', 42, at(600)));
      sink?.next(sample('humidity', 55, at(1)));
    });
    expect(screen.getByText('42')).toBeTruthy();
    expect(screen.getByText(/10 min/)).toBeTruthy();
    expect(screen.getByText('stale')).toBeTruthy();
  });

  it('adds nothing to a widget that did not opt in', () => {
    const { container } = render(
      <ConnectedWidget widget={widget('label', { text: 'Zone A' }, undefined)} hub={hubWith(1).hub} />,
    );
    expect(container.querySelector('[data-widget-freshness]')).toBeNull();
  });
});
