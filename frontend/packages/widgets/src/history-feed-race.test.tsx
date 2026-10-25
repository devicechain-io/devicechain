// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Overlapping reads of the history feed (a tab shown while a boundary read is out, a rebind): only the
// NEWEST read's outcome may land. An older one that finishes later must not replace newer buckets, raise
// its error over them, or move the age the frame reports.

import { GraphQLRequestError } from '@devicechain/client';
import type { WidgetDescriptor, WidgetInstance } from '@devicechain/dashboards';
import { useWidgetFreshness } from '@devicechain/widget-sdk';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import HistoryFeed from './history-feed';

interface Reply {
  ok: (buckets: unknown[]) => void;
  fail: (err: unknown) => void;
}

const widget = {
  id: 'review',
  type: 'history-table',
  datasource: { kind: 'device', deviceToken: 'pump', measurements: ['temp'] },
  options: {},
} as unknown as WidgetInstance;

function mount() {
  const replies: Reply[] = [];
  const hub = {
    bucketMeasurements: vi.fn(() => new Promise((ok, fail) => replies.push({ ok, fail } as Reply))),
  };
  const seen: { view?: any; at?: number | undefined } = {};
  const Component = (p: any) => {
    seen.view = p.history;
    seen.at = useWidgetFreshness()?.at;
    return null;
  };
  render(
    <HistoryFeed
      hub={hub as any}
      widget={widget}
      descriptor={{ binding: {}, freshness: true } as unknown as WidgetDescriptor}
      Component={Component}
      actions={undefined}
    />,
  );
  return { replies, seen };
}

const refresh = () => act(async () => void document.dispatchEvent(new Event('visibilitychange')));
const avg = (v: number) => [{ name: 'temp', avg: v }];

beforeEach(() => vi.useFakeTimers({ toFake: ['Date'] }));
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('history feed: the newest read wins', () => {
  it('keeps the newest buckets when refreshes finish out of order', async () => {
    const { replies, seen } = mount();
    await refresh();
    expect(replies).toHaveLength(2);
    await act(async () => replies[1].ok(avg(200)));
    expect(seen.view.buckets[0].avg).toBe(200);
    await act(async () => replies[0].ok(avg(100)));
    expect(seen.view.buckets[0].avg).toBe(200);
  });

  it('does not let an older failure show over a newer success', async () => {
    const { replies, seen } = mount();
    await refresh();
    await act(async () => replies[1].ok(avg(200)));
    await act(async () => replies[0].fail(new Error('boom')));
    expect(seen.view.status).toBe('ready');
    expect(seen.view.buckets[0].avg).toBe(200);
  });

  it('does not let an older refusal replace a newer success', async () => {
    const { replies, seen } = mount();
    await refresh();
    await act(async () => replies[1].ok(avg(200)));
    await act(async () => replies[0].fail(new GraphQLRequestError('forbidden', 403, [])));
    expect(seen.view.status).toBe('ready');
    expect(seen.view.buckets[0].avg).toBe(200);
  });

  it('does not let an older success hide a newer failure', async () => {
    const { replies, seen } = mount();
    await refresh();
    await act(async () => replies[1].fail(new Error('boom')));
    expect(seen.view.status).toBe('failed');
    await act(async () => replies[0].ok(avg(100)));
    expect(seen.view.status).toBe('failed');
  });

  it('does not reset the age when a stale read completes', async () => {
    const { replies, seen } = mount();
    await refresh();
    await act(async () => replies[1].ok(avg(200)));
    const landed = seen.at;
    expect(landed).toBeTypeOf('number');
    vi.setSystemTime(Date.now() + 90_000);
    await act(async () => replies[0].ok(avg(100)));
    expect(seen.at).toBe(landed);
  });
});
