// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from 'react';
import { GraphQLRequestError } from '@devicechain/client';

export interface QueryState<T> {
  status: 'loading' | 'success' | 'error';
  /** Failure of a refresh with display data still available. */
  refreshError: string | null;
  refetch: () => void;
  /**
   * DISPLAY value. Deliberately sticky: it keeps the previous answer while a new
   * refresh loads and after a failed refresh, so a view does not flicker or blank on a
   * re-read. Use it to RENDER. Never use it to ACT (enable a button, write a
   * value, pick a default). Without an explicit identity it can also be the
   * answer to a different question, for legacy callers.
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
  identity: unknown[] | undefined;
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
 * Pass identity separately from refresh dependencies to clear data immediately
 * on navigation. `status` describes the display; `loading` also covers refreshes.
 */
export function useQuery<T>(
  loader: () => Promise<T>,
  deps: unknown[] = [],
  // Opt in to clearing display data on identity changes. Refresh counters belong
  // in deps, not identity. Omitted for legacy callers that expect sticky display.
  identity?: unknown[],
): QueryState<T> {
  const [revision, setRevision] = useState(0);
  const refetch = useCallback(() => setRevision((value) => value + 1), []);
  const [state, setState] = useState<Internal<T>>({
    identity,
    data: null,
    loading: true,
    error: null,
    settled: undefined,
  });

  const matchesIdentity = identity === undefined ||
    (state.identity !== undefined && sameDeps(state.identity, identity));
  // Hide the previous entity in the render BEFORE the new effect runs too.
  const data = matchesIdentity ? state.data : null;
  const matchesRequest = matchesIdentity && state.settled !== undefined &&
    sameDeps(state.settled.deps, [...deps, revision]);

  useEffect(() => {
    let cancelled = false;
    setState((s) => ({ ...s, identity, data, loading: true, error: null, settled: undefined }));
    // Promise boundary catches synchronous loader failures as well as rejections.
    Promise.resolve().then(loader)
      .then((data) => {
        if (!cancelled) setState({ identity, data, loading: false, error: null, settled: { deps: [...deps, revision], value: data } });
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
        setState((s) => ({ ...s, loading: false, error: message, settled: undefined }));
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, revision, ...(identity ?? [])]);

  const current =
    !state.loading && state.error == null && matchesRequest && state.settled
      ? state.settled.value
      : undefined;
  const loading = !matchesIdentity || state.loading || (!state.error && !matchesRequest);
  const error = matchesIdentity ? state.error : null;
  const status = data !== null ? 'success' : loading ? 'loading' : error ? 'error' : 'success';
  return { data, current, loading, error, status, refetch,
    refreshError: data !== null ? error : null };
}
