// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { GraphQLRequestError } from '@devicechain/client';

export interface QueryState<T> {
  /**
   * DISPLAY value. Deliberately sticky: it keeps the previous answer while a new
   * key loads and after a failed load, so a view does not flicker or blank on a
   * re-read. Use it to RENDER. Never use it to ACT (enable a button, write a
   * value, pick a default) — it may be the answer to a different question.
   */
  data: T | null;
  /**
   * ACTION value. The answer to the CURRENT deps, and only that: `undefined`
   * while the current key is loading, after it failed, and in the render between
   * a deps change and its effect. A late response for an old key never lands
   * here. Gate every decision on this, not on `data`.
   */
  current: T | undefined;
  loading: boolean;
  error: string | null;
}

interface Internal<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  // The deps the last SUCCESSFUL load answered; undefined if none/cleared.
  settled: { deps: unknown[]; value: T } | undefined;
}

function sameDeps(a: unknown[], b: unknown[]): boolean {
  return a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
}

/**
 * Minimal fetch-on-mount hook for a GraphQL operation — the lightweight,
 * no-Apollo data pattern. `deps` re-runs the loader (defaults to once).
 *
 * Two views of the result, on purpose:
 *  - `data` is sticky (see above) — correct for display.
 *  - `current` is strict — correct for anything that acts on the value.
 */
export function useQuery<T>(loader: () => Promise<T>, deps: unknown[] = []): QueryState<T> {
  const [state, setState] = useState<Internal<T>>({
    data: null,
    loading: true,
    error: null,
    settled: undefined,
  });

  useEffect(() => {
    let cancelled = false;
    setState((s) => ({ ...s, loading: true, error: null, settled: undefined }));
    loader()
      .then((data) => {
        if (!cancelled) setState({ data, loading: false, error: null, settled: { deps, value: data } });
      })
      .catch((err) => {
        if (cancelled) return;
        const message =
          err instanceof GraphQLRequestError ? err.message : 'Failed to load data.';
        // Keep the prior data on error rather than blanking it: a page that
        // re-runs on a timer (e.g. a live-reconciling list) would otherwise flip
        // its whole view to an error state on one transient blip. Callers that gate
        // an error view on `data == null` keep showing stale-but-good rows; callers
        // that check `error` first are unaffected (data is present but unread).
        setState((s) => ({ data: s.data, loading: false, error: message, settled: undefined }));
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  const current =
    !state.loading && state.error == null && state.settled && sameDeps(state.settled.deps, deps)
      ? state.settled.value
      : undefined;
  return { data: state.data, current, loading: state.loading, error: state.error };
}
