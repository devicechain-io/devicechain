// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const vocab = vi.fn();
vi.mock('@/lib/api/device-management', () => ({ getDeviceMetricVocabulary: (t: string) => vocab(t) }));

import { useMetricUnits } from './use-metric-units';

beforeEach(() => {
  vocab.mockReset();
  vocab.mockImplementation(async (token: string) => [
    { metricKey: 'temperature', unit: token === 'a' ? '°C' : '°F' },
    { metricKey: 'raw', unit: null },
  ]);
});

describe('useMetricUnits', () => {
  it('looks up each device once and answers by device and metric', async () => {
    const { result, rerender } = renderHook(({ tokens }) => useMetricUnits(tokens), {
      initialProps: { tokens: ['a', 'a', null] as (string | null)[] },
    });
    await waitFor(() => expect(result.current('a', 'temperature')).toBe('°C'));
    expect(vocab).toHaveBeenCalledTimes(1);
    expect(result.current('a', 'raw')).toBeUndefined();
    expect(result.current('a', 'nope')).toBeUndefined();
    expect(result.current(null, 'temperature')).toBeUndefined();

    // A new device is read; the one already known is not read again.
    rerender({ tokens: ['b', 'a'] });
    await waitFor(() => expect(result.current('b', 'temperature')).toBe('°F'));
    expect(vocab.mock.calls.map((c) => c[0]).sort()).toEqual(['a', 'b']);
  });

  it('has no units for a device whose vocabulary cannot be read, and tries it again later', async () => {
    vocab.mockRejectedValueOnce(new Error('forbidden'));
    const { result, rerender } = renderHook(({ tokens }) => useMetricUnits(tokens), {
      initialProps: { tokens: ['a'] as string[] },
    });
    await waitFor(() => expect(vocab).toHaveBeenCalledTimes(1));
    expect(result.current('a', 'temperature')).toBeUndefined();
    // The set of devices changes, which re-runs the lookup; the failed one is asked again.
    rerender({ tokens: ['a', 'b'] });
    await waitFor(() => expect(result.current('a', 'temperature')).toBe('°C'));
  });

  it('treats a device with no vocabulary as having no units', async () => {
    vocab.mockResolvedValueOnce(null);
    const { result } = renderHook(() => useMetricUnits(['gone']));
    await waitFor(() => expect(vocab).toHaveBeenCalled());
    expect(result.current('gone', 'temperature')).toBeUndefined();
  });
});
