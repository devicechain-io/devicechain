// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import { installChunkReload } from '@/lib/chunkReload';

function host(storage: Map<string, string> | 'blocked') {
  const listeners: ((e: Event) => void)[] = [];
  const reload = vi.fn();
  const h = {
    addEventListener: (_: 'vite:preloadError', l: (e: Event) => void) => listeners.push(l),
    sessionStorage: {
      getItem: (k: string) => {
        if (storage === 'blocked') throw new Error('blocked');
        return storage.get(k) ?? null;
      },
      setItem: (k: string, v: string) => {
        if (storage === 'blocked') throw new Error('blocked');
        storage.set(k, v);
      },
    },
    location: { reload },
  };
  const fire = () => {
    const e = new Event('vite:preloadError', { cancelable: true });
    listeners.forEach((l) => l(e));
    return e;
  };
  return { h, reload, fire };
}

describe('reloading on a missing page chunk', () => {
  it('reloads once and swallows the failure', () => {
    const { h, reload, fire } = host(new Map());
    let t = 1_000_000;
    installChunkReload(h, () => t);
    const e = fire();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
    // A second failure soon after (the chunk is missing from the new deploy too) is left to the
    // error boundary rather than reloading again.
    t += 5_000;
    const again = fire();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(again.defaultPrevented).toBe(false);
    // Long after, a fresh deploy may have happened: reload again.
    t += 120_000;
    fire();
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it('never reloads when it cannot remember having tried', () => {
    const { h, reload, fire } = host('blocked');
    installChunkReload(h);
    expect(fire().defaultPrevented).toBe(false);
    expect(reload).not.toHaveBeenCalled();
  });
});
