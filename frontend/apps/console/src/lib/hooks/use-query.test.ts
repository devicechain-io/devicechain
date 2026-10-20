// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { useQuery } from './use-query';

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('useQuery data vs current', () => {
  it('success: data and current are both the answer', async () => {
    const { result } = renderHook(() => useQuery(() => Promise.resolve('a'), []));
    expect(result.current.current).toBeUndefined();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.data).toBe('a');
    expect(result.current.current).toBe('a');
  });

  it('key switch: data stays sticky, current is undefined until the new key answers', async () => {
    const pending: Record<string, ReturnType<typeof deferred<string>>> = {
      a: deferred<string>(),
      b: deferred<string>(),
    };
    const { result, rerender } = renderHook(({ k }) => useQuery(() => pending[k].promise, [k]), {
      initialProps: { k: 'a' },
    });
    await act(async () => pending.a.resolve('A'));
    expect(result.current.current).toBe('A');

    rerender({ k: 'b' });
    // Even in the render before the effect starts the new load, a stale answer is not current.
    expect(result.current.current).toBeUndefined();
    expect(result.current.data).toBe('A');
    expect(result.current.loading).toBe(true);

    await act(async () => pending.b.resolve('B'));
    expect(result.current.data).toBe('B');
    expect(result.current.current).toBe('B');
  });

  it('error: current is undefined, data keeps the previous answer', async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const { result, rerender } = renderHook(({ k }) => useQuery(() => (k === 1 ? first.promise : second.promise), [k]), {
      initialProps: { k: 1 },
    });
    await act(async () => first.resolve('A'));
    rerender({ k: 2 });
    await act(async () => second.reject(new Error('boom')));
    expect(result.current.error).toBe('Failed to load data.');
    expect(result.current.data).toBe('A');
    expect(result.current.current).toBeUndefined();
  });

  it('a late answer for an OLD key does not populate current', async () => {
    const old = deferred<string>();
    const fresh = deferred<string>();
    const { result, rerender } = renderHook(({ k }) => useQuery(() => (k === 1 ? old.promise : fresh.promise), [k]), {
      initialProps: { k: 1 },
    });
    rerender({ k: 2 });
    await act(async () => old.resolve('OLD'));
    expect(result.current.current).toBeUndefined();
    expect(result.current.data).toBeNull();
    expect(result.current.loading).toBe(true);

    await act(async () => fresh.resolve('NEW'));
    expect(result.current.current).toBe('NEW');
    expect(result.current.data).toBe('NEW');
  });
});
