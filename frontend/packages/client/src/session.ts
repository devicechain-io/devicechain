// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant session's access-token lifecycle, framework-free. A host owns WHERE
// the token pair lives (React state + localStorage in the console, memory in /dash)
// and WHAT the refresh call is; this owns WHEN to refresh and how concurrent callers
// share one refresh:
//
//   - getAccessToken() returns the held access token while it is comfortably valid,
//     and otherwise refreshes first.
//   - concurrent callers share ONE in-flight refresh. Refresh tokens rotate, so two
//     parallel refreshes would spend the same token twice and the second would fail.
//   - a result that arrives after the session was cleared or replaced is dropped, so a
//     slow refresh can never resurrect a dead session.
//   - a lapsed refresh token, or a failed refresh, ends the session through onEnd and
//     the caller gets null. The host decides what "ended" looks like.
//   - startAutoRefresh() refreshes ahead of expiry without waiting for a caller. A
//     display that only holds a websocket makes no request that would trigger the lazy
//     path, so without this it would sit on a token that is about to expire.

import { isExpired, decodeToken } from './jwt';

export interface TokenPair {
  accessToken: string;
  refreshToken: string;
}

export interface SessionTokensOptions {
  /** The held pair, or null when signed out. Must return the same object until it is replaced. */
  read: () => TokenPair | null;
  /** Stores a refreshed pair. */
  apply: (next: TokenPair) => void;
  /** Exchanges a refresh token for a new pair. */
  refresh: (refreshToken: string) => Promise<TokenPair>;
  /**
   * The session can no longer be sustained: the refresh token lapsed (`err` undefined) or the
   * refresh call failed (`err` is what it threw). The host clears its session here.
   */
  onEnd: (err?: unknown) => void;
  /** Refresh this many seconds before the access token expires. Default 30. */
  skewSeconds?: number;
}

export interface SessionTokens {
  /** A usable access token, refreshing first when it is within the skew of expiry; null when signed out or ended. */
  getAccessToken: () => Promise<string | null>;
  /** Forget any in-flight refresh (the host cleared or replaced the session itself). */
  reset: () => void;
  /** Refresh ahead of expiry on a timer. Returns a stop function. */
  startAutoRefresh: () => () => void;
}

const DEFAULT_SKEW_SECONDS = 30;
// setTimeout stores its delay in a signed 32-bit int; a longer one fires immediately.
const MAX_TIMER_MS = 2 ** 31 - 1;

export function createSessionTokens(opts: SessionTokensOptions): SessionTokens {
  const skew = opts.skewSeconds ?? DEFAULT_SKEW_SECONDS;
  let inFlight: Promise<string | null> | null = null;

  const getAccessToken = async (): Promise<string | null> => {
    const current = opts.read();
    if (!current) return null;
    if (isExpired(current.refreshToken, 0)) {
      inFlight = null;
      opts.onEnd();
      return null;
    }
    if (!isExpired(current.accessToken, skew)) return current.accessToken;

    if (!inFlight) {
      inFlight = (async () => {
        try {
          const pair = await opts.refresh(current.refreshToken);
          // A promise cannot be cancelled, so guard here: if the live session is no
          // longer the one this refresh started from, drop the result.
          if (opts.read() !== current) return null;
          opts.apply({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
          return pair.accessToken;
        } catch (err) {
          // Only end the session this refresh belonged to, not one since replaced.
          if (opts.read() === current) {
            inFlight = null;
            opts.onEnd(err);
          }
          return null;
        } finally {
          inFlight = null;
        }
      })();
    }
    return inFlight;
  };

  const startAutoRefresh = (): (() => void) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;

    const schedule = () => {
      if (stopped) return;
      const current = opts.read();
      const exp = current ? decodeToken(current.accessToken)?.exp : undefined;
      if (!current || exp === undefined) return; // signed out, or nothing to time against
      const delay = Math.min(Math.max(0, (exp - skew) * 1000 - Date.now()), MAX_TIMER_MS);
      timer = setTimeout(() => {
        void getAccessToken().then((token) => {
          // Re-arm only on success: a null means the session ended (or was cleared).
          if (token !== null) schedule();
        });
      }, delay);
    };

    schedule();
    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  };

  return {
    getAccessToken,
    reset: () => {
      inFlight = null;
    },
    startAutoRefresh,
  };
}
