// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { GraphQLRequestError } from '@devicechain/client';

interface QueryState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
}

/**
 * Minimal fetch-on-mount hook for a GraphQL operation — the lightweight,
 * no-Apollo data pattern. `deps` re-runs the loader (defaults to once).
 */
export function useQuery<T>(loader: () => Promise<T>, deps: unknown[] = []): QueryState<T> {
  const [state, setState] = useState<QueryState<T>>({ data: null, loading: true, error: null });

  useEffect(() => {
    let cancelled = false;
    setState((s) => ({ ...s, loading: true, error: null }));
    loader()
      .then((data) => {
        if (!cancelled) setState({ data, loading: false, error: null });
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
        setState((s) => ({ data: s.data, loading: false, error: message }));
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return state;
}