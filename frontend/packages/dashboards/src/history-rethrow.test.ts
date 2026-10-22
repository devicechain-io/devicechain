// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// fetchWidgetHistory's failure modes. The first read of a widget turns a failure into an empty seed
// (the live stream still fills the chart). A re-read after the live feed was interrupted asks for
// `rethrow`: an empty answer there would be taken as history across the outage, and would wipe
// what the chart drew before it.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { fetchWidgetHistory } from './history';
import type { WidgetInstance } from './types';

const WINDOW = { startTime: '2026-10-14T11:00:00.000Z', endTime: '2026-10-14T12:00:00.000Z', intervalSeconds: 60 };
const widget = {
  id: 'w',
  type: 'timeseries-chart',
  layout: {},
  datasource: { kind: 'device', deviceToken: 'd-1', measurements: ['t'] },
  options: {},
} as unknown as WidgetInstance;

beforeEach(() => {
  h.gql.mockReset();
});

describe('fetchWidgetHistory when the read fails', () => {
  it('answers an empty seed by default', async () => {
    h.gql.mockRejectedValue(new Error('event-management unreachable'));
    await expect(fetchWidgetHistory(widget, WINDOW)).resolves.toEqual([]);
  });

  it('rejects with the failure when asked to rethrow', async () => {
    h.gql.mockRejectedValue(new Error('event-management unreachable'));
    await expect(fetchWidgetHistory(widget, WINDOW, { rethrow: true })).rejects.toThrow('event-management unreachable');
  });

  it('marks each seeded point as a bucket average of the width it was read in', async () => {
    h.gql.mockResolvedValue({ bucketedMeasurements: [{ name: 't', bucketStart: '2026-10-14T11:00:00Z', avg: 4 }] });
    const seed = await fetchWidgetHistory(widget, { ...WINDOW, intervalSeconds: 360 });
    expect(seed.map((s) => s.bucketSeconds)).toEqual([360]);
  });

  it('answers the buckets either way when the read succeeds', async () => {
    h.gql.mockResolvedValue({ bucketedMeasurements: [{ name: 't', bucketStart: '2026-10-14T11:00:00Z', avg: 4 }] });
    const seed = await fetchWidgetHistory(widget, WINDOW, { rethrow: true });
    expect(seed.map((s) => [s.occurredTime, s.value])).toEqual([['2026-10-14T11:00:00Z', 4]]);
  });
});
