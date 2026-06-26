// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from 'react';

interface UseLocalStorageOptions<T> {
  serialize?: (value: T) => string;
  deserialize?: (raw: string) => T;
}

/**
 * SSR-safe localStorage-backed state hook. Default serializer is JSON;
 * pass a serializer pair to override (e.g. bool-as-`'1'`/`'0'`).
 *
 * Returns the same `[state, setState]` shape as `useState` — `setState`
 * accepts either a value or an updater.
 */
export function useLocalStorage<T>(
  key: string,
  initial: T,
  options?: UseLocalStorageOptions<T>,
): [T, (next: T | ((prev: T) => T)) => void] {
  const serialize = options?.serialize ?? JSON.stringify;
  const deserialize = options?.deserialize ?? (JSON.parse as (raw: string) => T);

  const [value, setValue] = useState<T>(() => {
    if (typeof window === 'undefined') return initial;
    try {
      const raw = window.localStorage.getItem(key);
      return raw == null ? initial : deserialize(raw);
    } catch {
      return initial;
    }
  });

  useEffect(() => {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(key, serialize(value));
    } catch {
      // quota / disabled — silent
    }
  }, [key, value, serialize]);

  const update = useCallback((next: T | ((prev: T) => T)) => {
    setValue((prev) => (typeof next === 'function' ? (next as (p: T) => T)(prev) : next));
  }, []);

  return [value, update];
}