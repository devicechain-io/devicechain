// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';

// Stale-while-revalidate cache for a rarely-changing per-session resource (the
// current tenant, the signed-in user): render from localStorage immediately,
// then refresh from the server in the background. `cacheKey` is the full storage
// key — callers namespace it by identity (e.g. `dc-tenant:<token>`); a null key
// means "no resource" and clears the value. A failed refresh keeps whatever was
// cached, so a transient error never blanks the UI.
export function useCachedResource<T>(cacheKey: string | null, fetcher: () => Promise<T>): T | null {
  // Keep the latest fetcher without making it an effect dependency: the effect
  // re-runs only when the cache key changes, not on every render.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const [value, setValue] = useState<T | null>(() => (cacheKey ? read<T>(cacheKey) : null));

  useEffect(() => {
    if (!cacheKey) {
      setValue(null);
      return;
    }
    const cached = read<T>(cacheKey);
    if (cached) setValue(cached);
    let cancelled = false;
    fetcherRef
      .current()
      .then((next) => {
        if (cancelled) return;
        setValue(next);
        write(cacheKey, next);
      })
      .catch(() => {
        // keep the cached value
      });
    return () => {
      cancelled = true;
    };
  }, [cacheKey]);

  return value;
}

function read<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function write<T>(key: string, value: T) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // storage disabled / quota — falls back to a live fetch each load
  }
}
