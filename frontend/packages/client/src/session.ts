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
//   - a lapsed refresh token, or a DEFINITIVE refresh failure (the server answered and
//     refused: a GraphQL error such as an invalid, revoked or SSO-gated refresh token, or
//     any 4xx but 429), ends the session through onEnd and the caller gets null. The host
//     decides what "ended" looks like.
//   - a TRANSIENT refresh failure (no response at all, a 5xx, or a 429) does not end a
//     session whose access token is still valid: the caller is handed that token, and the
//     next call (or the auto-refresh retry below) tries again. Only once the access token
//     has actually expired does a transient failure end the session, since there is then
//     nothing left to authenticate with.
//   - startAutoRefresh() refreshes ahead of expiry without waiting for a caller, and
//     retries a transient failure with jittered exponential backoff until the access
//     token actually expires. A display that only holds a websocket makes no request that
//     would trigger the lazy path, so without this it would sit on a token that is about
//     to expire, and a network blip at the wrong moment would end an unattended session.

import { isExpired, decodeToken } from './jwt';
import { GraphQLRequestError } from './transport';

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
   * The session can no longer be sustained: the refresh token lapsed (`err` undefined), the
   * server refused the refresh (`err` is what it threw), or transient failures outlasted the
   * access token (`err` is the last one). The host clears its session here.
   */
  onEnd: (err?: unknown) => void;
  /**
   * Refresh this many seconds before the access token expires. Default 30. A larger value
   * gives startAutoRefresh's retries a longer window to ride out a network outage. It is
   * capped at half the token's lifetime (`exp - iat`), so a server configured with short-lived
   * tokens never hands out one that is already due, which would refresh on every call.
   */
  skewSeconds?: number;
}

export interface SessionTokens {
  /** A usable access token, refreshing first when it is within the skew of expiry; null when signed out or ended. */
  getAccessToken: () => Promise<string | null>;
  /** Forget any in-flight refresh (the host cleared or replaced the session itself). */
  reset: () => void;
  /**
   * Refresh ahead of expiry on a timer, retrying a transient failure with backoff until the
   * access token expires. Returns a stop function.
   */
  startAutoRefresh: () => () => void;
}

const DEFAULT_SKEW_SECONDS = 30;
// setTimeout stores its delay in a signed 32-bit int; a longer one fires immediately.
const MAX_TIMER_MS = 2 ** 31 - 1;
// Auto-refresh retry backoff: 1 s doubling to 30 s, each scaled by a random 50-100 % so a
// fleet of displays that lost the network together does not come back in lockstep.
const RETRY_BASE_MS = 1_000;
const RETRY_CAP_MS = 30_000;
// The last retry is aimed this far ahead of the access token's expiry, not at it. No retry is
// armed within RETRY_BASE_MS before that aim: one that would be is moved onto it instead.
const RETRY_EXPIRY_MARGIN_MS = 1_000;
// The shortest gap between two scheduled refreshes. A backstop: if the held token still
// looks due right after a refresh (the device clock runs well ahead of the server's), the
// timer must not turn into a refresh loop against the server.
const MIN_REFRESH_GAP_MS = 10_000;

/**
 * Whether a refresh failure is worth retrying: the request never got a response (status 0),
 * or the server answered 5xx or 429 without a GraphQL error. Anything else (a GraphQL error
 * such as an invalid or revoked refresh token or SSO_REQUIRED, a 400 or 401, or an error that
 * is not a transport error at all) is the server's answer, and asking again would repeat it.
 */
function isTransientRefreshError(err: unknown): boolean {
  if (!(err instanceof GraphQLRequestError)) return false;
  if (err.errors && err.errors.length > 0) return false;
  return err.status === 0 || err.status === 429 || err.status >= 500;
}

type Outcome =
  | { kind: 'ok'; token: string }
  | { kind: 'stale' } // the session was cleared or replaced while the refresh ran
  | { kind: 'ended' }
  | { kind: 'transient'; err: unknown };

function expiryMs(token: string): number | undefined {
  const exp = decodeToken(token)?.exp;
  return exp === undefined ? undefined : exp * 1000;
}

export function createSessionTokens(opts: SessionTokensOptions): SessionTokens {
  const skew = opts.skewSeconds ?? DEFAULT_SKEW_SECONDS;
  let inFlight: Promise<Outcome> | null = null;

  // The skew for one access token: the configured one, or half its lifetime if that is less.
  const skewFor = (token: string): number => {
    const c = decodeToken(token);
    if (c?.exp === undefined || c.iat === undefined) return skew;
    return Math.min(skew, Math.max(0, (c.exp - c.iat) / 2));
  };
  const due = (token: string): boolean => isExpired(token, skewFor(token));

  const end = (err?: unknown) => {
    inFlight = null;
    opts.onEnd(err);
  };

  // One refresh of `current`, shared by every caller that asks while it runs.
  const refreshShared = (current: TokenPair): Promise<Outcome> => {
    if (!inFlight) {
      inFlight = (async (): Promise<Outcome> => {
        try {
          const pair = await opts.refresh(current.refreshToken);
          // A promise cannot be cancelled, so guard here: if the live session is no
          // longer the one this refresh started from, drop the result.
          if (opts.read() !== current) return { kind: 'stale' };
          opts.apply({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
          return { kind: 'ok', token: pair.accessToken };
        } catch (err) {
          // Only end the session this refresh belonged to, not one since replaced.
          if (opts.read() !== current) return { kind: 'stale' };
          if (isTransientRefreshError(err)) return { kind: 'transient', err };
          end(err);
          return { kind: 'ended' };
        } finally {
          inFlight = null;
        }
      })();
    }
    return inFlight;
  };

  const getAccessToken = async (): Promise<string | null> => {
    const current = opts.read();
    if (!current) return null;
    if (isExpired(current.refreshToken, 0)) {
      end();
      return null;
    }
    if (!due(current.accessToken)) return current.accessToken;

    const out = await refreshShared(current);
    if (out.kind === 'ok') return out.token;
    if (out.kind !== 'transient') return null;
    // A transient failure: the held token is still good if it has not actually expired.
    if (opts.read() !== current) return null;
    if (!isExpired(current.accessToken, 0)) return current.accessToken;
    end(out.err);
    return null;
  };

  const startAutoRefresh = (): (() => void) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    let failures = 0;

    const arm = (delay: number) => {
      timer = setTimeout(() => void fire(), Math.min(Math.max(0, delay), MAX_TIMER_MS));
    };

    // Arm for the next refresh of whatever pair is held now, no sooner than `floor` ms.
    const schedule = (floor = 0) => {
      if (stopped) return;
      const current = opts.read();
      const exp = current ? expiryMs(current.accessToken) : undefined;
      if (!current || exp === undefined) return; // signed out, or nothing to time against
      arm(Math.max(floor, exp - skewFor(current.accessToken) * 1000 - Date.now()));
    };

    const fire = async () => {
      if (stopped) return;
      const current = opts.read();
      if (!current) return;
      if (isExpired(current.refreshToken, 0)) {
        end();
        return;
      }
      // Someone else refreshed already (a request took the lazy path): time against that.
      if (!due(current.accessToken)) {
        failures = 0;
        schedule(MIN_REFRESH_GAP_MS);
        return;
      }
      const out = await refreshShared(current);
      if (stopped) return;
      if (out.kind === 'ok') {
        failures = 0;
        schedule(MIN_REFRESH_GAP_MS);
        return;
      }
      if (out.kind !== 'transient') return; // ended, or the host replaced the session
      if (opts.read() !== current) return;

      const left = (expiryMs(current.accessToken) ?? 0) - RETRY_EXPIRY_MARGIN_MS - Date.now();
      if (left <= 0) {
        end(out.err);
        return;
      }
      const backoff = Math.min(RETRY_CAP_MS, RETRY_BASE_MS * 2 ** failures) * (0.5 + Math.random() / 2);
      failures++;
      // A retry that would land within RETRY_BASE_MS of the final retry's aim (or past it) is
      // moved onto that aim. Otherwise a backoff that happens to land just short of the aim leaves
      // the aimed retry following it moments later: two requests back to back, against a
      // server that may have just answered 429.
      arm(left - backoff < RETRY_BASE_MS ? left : backoff);
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
