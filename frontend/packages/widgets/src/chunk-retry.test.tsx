// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A widget whose chunk failed to download recovers on "Reset widget". The failure used to be
// remembered (a memoized rejected promise here, React.lazy for the feeds), so every Reset awaited
// the same rejection and threw it again: on a wall display one network blip at first load left the
// widget broken until someone reloaded the page.

import type { DashboardHub, DatasourceSelector, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import type { ComponentType } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WidgetContainment } from './widget-containment';
import { lazyChunk } from './widgets/lazy-chunk';

afterEach(() => {
  cleanup();
  vi.doUnmock('./aggregate-feed');
  vi.doUnmock('./fleet-feed');
  vi.resetModules();
});

// The boundary logs what it caught; that is expected here.
const quiet = () => vi.spyOn(console, 'error').mockImplementation(() => {});

async function resetOnce(): Promise<void> {
  expect(await screen.findByText('This widget failed to render', {}, { timeout: 10_000 })).toBeTruthy();
  await act(async () => void fireEvent.click(screen.getByText('Reset widget')));
}

describe('a chunk that failed to load', () => {
  it('is fetched again on Reset, and the widget renders', async () => {
    quiet();
    const importer = vi
      .fn<() => Promise<{ Shown: ComponentType }>>()
      .mockRejectedValueOnce(new Error('Failed to fetch dynamically imported module'))
      .mockResolvedValue({ Shown: () => <p>chunk arrived</p> });
    const Lazy = lazyChunk(importer).component<never, object>('Shown');

    render(
      <WidgetContainment resetKey="k">
        <Lazy />
      </WidgetContainment>,
    );
    await resetOnce();
    expect(await screen.findByText('chunk arrived')).toBeTruthy();
    expect(importer).toHaveBeenCalledTimes(2);
  }, 30_000);

  it('is not fetched again while nothing asks for it', async () => {
    quiet();
    const importer = vi.fn<() => Promise<{ Shown: ComponentType }>>().mockRejectedValue(new Error('gone'));
    const Lazy = lazyChunk(importer).component<never, object>('Shown');
    render(
      <WidgetContainment resetKey="k">
        <Lazy />
      </WidgetContainment>,
    );
    expect(await screen.findByText('This widget failed to render')).toBeTruthy();
    await act(async () => void (await new Promise((r) => setTimeout(r, 100))));
    expect(importer).toHaveBeenCalledTimes(1);
  });
});

const widget = (type: string, datasource: DatasourceSelector): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource,
    options: { title: 'T', measurement: 'temperature' },
  }) as unknown as WidgetInstance;

const hub = {
  subscribeWidget: () => () => {},
  subscribeMembers: () => () => {},
  bucketMeasurements: async () => [],
  readMemberSeeds: async () => {},
  isDatasourceAvailable: async () => true,
} as unknown as DashboardHub;

// The two feeds the connected widget loads as chunks of their own. Each mock fails the first
// time the module is asked for and serves a stand-in after that.
describe.each([
  ['./aggregate-feed', 'stat-card', { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] }],
  ['./fleet-feed', 'fleet-table', { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' }, measurements: ['temperature'] }],
] as const)('the %s chunk', (path, type, datasource) => {
  it('is fetched again on Reset, and the widget renders', async () => {
    quiet();
    vi.resetModules();
    let asked = 0;
    vi.doMock(path, () => {
      if (asked++ === 0) throw new Error('Failed to fetch dynamically imported module');
      return { default: () => <p>feed arrived</p> };
    });
    const { ConnectedWidget } = await import('./connected-widget');

    render(<ConnectedWidget widget={widget(type, datasource as unknown as DatasourceSelector)} hub={hub} />);
    await resetOnce();
    expect(await screen.findByText('feed arrived', {}, { timeout: 5_000 })).toBeTruthy();
    expect(asked).toBe(2);
  }, 30_000);
});
