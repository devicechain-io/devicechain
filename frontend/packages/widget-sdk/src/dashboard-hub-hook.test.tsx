// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A host builds ONE hub per (resolver, authorities) and keeps it across selections. The
// hub carries no bindings, so nothing about a selection can make a host rebuild it; this
// pins the other two inputs and the teardown.

import { DashboardHub, type DeviceResolver } from '@devicechain/dashboards';
import { cleanup, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { useDashboardHub } from './hooks';

afterEach(cleanup);

const resolver = (): DeviceResolver => ({ devicesForAnchor: async () => [], deviceExists: async () => true });

describe('useDashboardHub', () => {
  it('keeps one hub while the resolver and the authorities (by value) are unchanged', () => {
    const r = resolver();
    const { result, rerender } = renderHook(({ authorities }) => useDashboardHub(r, authorities), {
      initialProps: { authorities: ['alarm:write'] },
    });
    const first = result.current;
    expect(first).toBeInstanceOf(DashboardHub);

    rerender({ authorities: ['alarm:write'] }); // a new array, equal by value
    expect(result.current).toBe(first);
  });

  it('builds a new hub when the authorities change, and disposes the old one', () => {
    const r = resolver();
    const { result, rerender } = renderHook(({ authorities }) => useDashboardHub(r, authorities), {
      initialProps: { authorities: ['alarm:write'] as string[] | undefined },
    });
    const first = result.current;
    const dispose = vi.spyOn(first, 'disposeAll');

    rerender({ authorities: ['alarm:write', 'command:write'] });
    expect(result.current).not.toBe(first);
    expect(result.current.can('command:write')).toBe(true);
    expect(dispose).toHaveBeenCalledTimes(1);
  });

  it('builds a new hub for a new resolver', () => {
    const { result, rerender } = renderHook(({ r }) => useDashboardHub(r, undefined), {
      initialProps: { r: resolver() },
    });
    const first = result.current;
    rerender({ r: resolver() });
    expect(result.current).not.toBe(first);
  });

  it('disposes the hub on unmount', () => {
    const { result, unmount } = renderHook(() => useDashboardHub(resolver(), undefined));
    const dispose = vi.spyOn(result.current, 'disposeAll');
    unmount();
    expect(dispose).toHaveBeenCalledTimes(1);
  });
});
