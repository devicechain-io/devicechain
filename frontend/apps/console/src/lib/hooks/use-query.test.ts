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
    // rerender flushes the effect, so this is the state once the new load has started; the
    // render BEFORE the effect is covered by the render-recording test below.
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

  // Out of order: the new key answers FIRST, then the old one. The late answer must not
  // overwrite either view.
  it('an OLD-key answer arriving after the new one overwrites neither data nor current', async () => {
    const old = deferred<string>();
    const fresh = deferred<string>();
    const { result, rerender } = renderHook(({ k }) => useQuery(() => (k === 1 ? old.promise : fresh.promise), [k]), {
      initialProps: { k: 1 },
    });
    rerender({ k: 2 });
    await act(async () => fresh.resolve('NEW'));
    await act(async () => old.resolve('OLD'));
    expect(result.current.current).toBe('NEW');
    expect(result.current.data).toBe('NEW');
  });

  // renderHook's rerender flushes effects inside act, so asserting after it only sees the
  // state AFTER the new load started. This records every render instead, which is the only
  // way to see the one render between a deps change and its effect.
  it('in the render between a deps change and its effect, current is already undefined', async () => {
    const seen: { k: string; current: string | undefined }[] = [];
    const { rerender } = renderHook(
      ({ k }) => {
        const r = useQuery(() => Promise.resolve(k.toUpperCase()), [k]);
        seen.push({ k, current: r.current });
        return r;
      },
      { initialProps: { k: 'a' } },
    );
    await waitFor(() => expect(seen.at(-1)?.current).toBe('A'));
    rerender({ k: 'b' });
    await waitFor(() => expect(seen.at(-1)?.current).toBe('B'));
    const underB = seen.filter((s) => s.k === 'b');
    expect(underB.length).toBeGreaterThan(1);
    expect(underB.some((s) => s.current === 'A')).toBe(false);
  });

  it('a re-render with equal deps keeps current (no flicker on an unrelated render)', async () => {
    const { result, rerender } = renderHook(({ k }) => useQuery(() => Promise.resolve(`v-${k}`), [k, 'x']), {
      initialProps: { k: 'a' },
    });
    await waitFor(() => expect(result.current.current).toBe('v-a'));
    rerender({ k: 'a' });
    expect(result.current.current).toBe('v-a');
    expect(result.current.loading).toBe(false);
  });
});
