// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Overlapping aggregate reads (a tab shown while a boundary read is out): only the NEWEST read's outcome
// lands. An older one finishing later must not replace the newer figures or the newer failure.

import type { WidgetDescriptor, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import AggregateFeed from './aggregate-feed';

interface Reply {
  ok: (buckets: unknown[]) => void;
  fail: (err: unknown) => void;
}

const widget = {
  id: 'review',
  type: 'stat-card',
  datasource: { kind: 'device', deviceToken: 'pump', measurements: ['temp'] },
  options: {},
} as unknown as WidgetInstance;

const bucket = (avg: number) => [{ bucketStart: '2026-10-14T11:00:00Z', name: 'temp', avg, min: avg, max: avg, sum: avg, count: 1 }];

function mount() {
  const replies: Reply[] = [];
  const hub = {
    subscribeWidget: () => () => {},
    bucketMeasurements: vi.fn(() => new Promise((ok, fail) => replies.push({ ok, fail } as Reply))),
  };
  const seen: { aggregates?: any } = {};
  const Component = (p: any) => {
    seen.aggregates = p.data.aggregates;
    return null;
  };
  render(
    <AggregateFeed
      hub={hub as any}
      widget={widget}
      descriptor={{ binding: {} } as unknown as WidgetDescriptor}
      Component={Component}
      actions={undefined}
      ErrorFrame={() => null}
    />,
  );
  return { replies, seen };
}

const refresh = () => act(async () => void document.dispatchEvent(new Event('visibilitychange')));

afterEach(cleanup);

describe('aggregate feed: the newest read wins', () => {
  it('keeps the newest figures when refreshes finish out of order', async () => {
    const { replies, seen } = mount();
    await refresh();
    expect(replies).toHaveLength(2);
    await act(async () => replies[1].ok(bucket(200)));
    expect(seen.aggregates.status).toBe('ready');
    const newest = seen.aggregates.byName;
    await act(async () => replies[0].ok(bucket(100)));
    expect(seen.aggregates.byName).toBe(newest);
  });

  it('does not let an older failure replace newer figures', async () => {
    const { replies, seen } = mount();
    await refresh();
    await act(async () => replies[1].ok(bucket(200)));
    await act(async () => replies[0].fail(new Error('boom')));
    expect(seen.aggregates.status).toBe('ready');
  });

  it('does not let an older success hide a newer failure', async () => {
    const { replies, seen } = mount();
    await refresh();
    await act(async () => replies[1].fail(new Error('boom')));
    expect(seen.aggregates.status).toBe('unavailable');
    await act(async () => replies[0].ok(bucket(100)));
    expect(seen.aggregates.status).toBe('unavailable');
  });
});
