// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { DEVICE_NAMES, DEVICES_BY_TOKEN, type DeviceNamesResult } from './queries';
import { createDeviceResolver } from './resolver';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

const pump: DeviceNamesResult = {
  devicesByToken: [{ token: 'device-1', name: 'Pump 1', deviceType: { token: 'pump' } }],
};

beforeEach(() => {
  h.gql.mockReset();
});

describe('deviceType caching', () => {
  it('recovers when a missing device appears on the same resolver', async () => {
    h.gql.mockResolvedValueOnce({ devicesByToken: [] }).mockResolvedValueOnce(pump);
    const resolver = createDeviceResolver();

    expect(await resolver.deviceType!('device-1')).toBeNull();
    expect(await resolver.deviceType!('device-1')).toBe('pump');
    expect(h.gql).toHaveBeenCalledTimes(2);
    expect(h.gql).toHaveBeenNthCalledWith(1, 'device-management', DEVICE_NAMES, { tokens: ['device-1'] });
    expect(h.gql).toHaveBeenNthCalledWith(2, 'device-management', DEVICE_NAMES, { tokens: ['device-1'] });
  });

  it('recovers when an existing device is assigned a type', async () => {
    h.gql.mockResolvedValueOnce({
      devicesByToken: [{ token: 'device-1', name: 'Pump 1', deviceType: null }],
    }).mockResolvedValueOnce(pump);
    const resolver = createDeviceResolver();

    expect(await resolver.deviceType!('device-1')).toBeNull();
    expect(await resolver.deviceType!('device-1')).toBe('pump');
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('coalesces concurrent initial lookups while a request is pending', async () => {
    let resolve!: (value: DeviceNamesResult) => void;
    h.gql.mockReturnValueOnce(new Promise<DeviceNamesResult>((done) => { resolve = done; }));
    const resolver = createDeviceResolver();

    const first = resolver.deviceType!('device-1');
    const second = resolver.deviceType!('device-1');
    expect(second).toBe(first);
    expect(h.gql).toHaveBeenCalledTimes(1);

    resolve({ devicesByToken: [] });
    expect(await Promise.all([first, second])).toEqual([null, null]);
    expect(h.gql).toHaveBeenCalledTimes(1);
  });

  it('memoizes a positive result across sequential lookups', async () => {
    h.gql.mockResolvedValue(pump);
    const resolver = createDeviceResolver();

    expect(await resolver.deviceType!('device-1')).toBe('pump');
    expect(await resolver.deviceType!('device-1')).toBe('pump');
    expect(h.gql).toHaveBeenCalledTimes(1);
  });

  it('evicts a rejected lookup so the next call can retry', async () => {
    const error = new Error('device-management unavailable');
    h.gql.mockRejectedValueOnce(error).mockResolvedValueOnce(pump);
    const resolver = createDeviceResolver();

    await expect(resolver.deviceType!('device-1')).rejects.toBe(error);
    expect(await resolver.deviceType!('device-1')).toBe('pump');
    expect(h.gql).toHaveBeenCalledTimes(2);
  });
});

describe('deviceExists caching control', () => {
  it('recovers from false to true on the same resolver and memoizes true', async () => {
    h.gql.mockResolvedValueOnce({ devicesByToken: [] })
      .mockResolvedValueOnce({ devicesByToken: [{ token: 'device-1' }] });
    const resolver = createDeviceResolver();

    expect(await resolver.deviceExists!('device-1')).toBe(false);
    expect(await resolver.deviceExists!('device-1')).toBe(true);
    expect(await resolver.deviceExists!('device-1')).toBe(true);
    expect(h.gql).toHaveBeenCalledTimes(2);
    expect(h.gql).toHaveBeenNthCalledWith(1, 'device-management', DEVICES_BY_TOKEN, { tokens: ['device-1'] });
    expect(h.gql).toHaveBeenNthCalledWith(2, 'device-management', DEVICES_BY_TOKEN, { tokens: ['device-1'] });
  });
});
