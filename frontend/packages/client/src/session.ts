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
//   - a TRANSIENT refresh failure (no response at all, a 5xx, or a 429) NEVER ends the
//     session: the server has not refused anything, and the refresh token is still good. While
//     the access token is valid the caller is handed it. Once it has expired there is nothing
//     valid to hand over, and what the caller gets depends on `transientRefreshErrors`:
//       'null' (the default, and the v1.6 contract of getAccessToken): null, exactly as v1.6
//         returned. v1.6 also ended the session there; it no longer does, so the next call
//         (or the auto-refresh retry below) tries again.
//       'throw' (opt-in): the transient error is thrown, so the request fails with the outage
//         it hit rather than going out without a token and reading the server's 401 as a
//         refusal. The console and /dash opt in.
//     Only the refresh token lapsing ends a session that keeps failing transiently.
//     🔴 #2752 made 'throw' the only behaviour. getAccessToken is a published export and a
//     v1.6 caller checks for null, so a promise that now rejected was a contract change in a
//     minor: it became the opt-in.
//   - exchange() runs a host operation that SPENDS the refresh token for its own reasons (the
//     console's step-up trades it, with a second-factor code, for an elevated pair) through the
//     same coordinator: it waits for a refresh already running, and while it runs, a refresh
//     anyone asks for (a due token, a 401, the auto-refresh timer) waits for it and takes the
//     pair it yields instead of spending the same token again. 🔴 Before it, a forced refresh
//     after a 401 and a step-up could spend one refresh token together: the server honours only
//     one, the loser's refusal ended the session, and the user was signed out after verifying.
//   - startAutoRefresh() refreshes ahead of expiry without waiting for a caller, and
//     retries a transient failure with jittered exponential backoff for as long as the
//     refresh token is valid. A display that only holds a websocket makes no request that
//     would trigger the lazy path, so without this it would sit on a token that is about
//     to expire, and a network blip at the wrong moment would end an unattended session.
//     🔴 It used to give up when the ACCESS token expired, so an outage longer than the
//     access token's few minutes signed out a wall display whose refresh token had hours
//     left, and nobody was there to sign it back in.

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
   * The session can no longer be sustained: the refresh token lapsed (`err` undefined), or the
   * server refused the refresh (`err` is what it threw). A transient failure never ends it. The
   * host clears its session here.
   */
  onEnd: (err?: unknown) => void;
  /**
   * Refresh this many seconds before the access token expires. Default 30. A larger value
   * gives startAutoRefresh's retries a longer window to ride out a network outage. It is
   * capped at half the token's lifetime (`exp - iat`), so a server configured with short-lived
   * tokens never hands out one that is already due, which would refresh on every call.
   */
  skewSeconds?: number;
  /**
   * What `getAccessToken` does when the access token has expired and the refresh failed
   * transiently (no response, a 5xx or a 429). Either way the session is kept and a later call
   * tries again; `onEnd` is not called.
   *
   * - `'null'` (default): resolve to null, as in v1.6.
   * - `'throw'`: reject with the refresh's error, so the request fails with the outage instead
   *   of being sent without a token. `subscribe` treats that error as transient: the
   *   subscription's reconnect retries it.
   */
  transientRefreshErrors?: 'null' | 'throw';
}

export interface SessionTokens {
  /**
   * Force one shared refresh after a 401, even though the held access token has not expired.
   * Resolves to the refreshed token, or null when the session is signed out, was replaced since
   * `rejectedToken` was sent, or has ended (the refresh token lapsed or the server refused the
   * refresh). Rejects with the refresh's error when it failed transiently: the session is kept,
   * and a later call tries again. Never hands back the rejected token. It rejects whatever
   * `transientRefreshErrors` says: a null here tells `setUnauthorizedHandler` the session ended.
   *
   * Optional HERE because v1.6 had no such member, and a host that builds its own
   * `SessionTokens` must still type-check. What `createSessionTokens` returns always has it:
   * see `ManagedSessionTokens`. Without it a host has no 401 recovery, as in v1.6.
   */
  forceRefresh?: (rejectedToken?: string) => Promise<string | null>;
  /**
   * A usable access token, refreshing first when it is within the skew of expiry; null when
   * signed out or ended. When the access token has expired and the refresh failed transiently,
   * the session is kept and a later call tries again; the call resolves to null, or rejects with
   * the refresh's error under `transientRefreshErrors: 'throw'`.
   */
  getAccessToken: () => Promise<string | null>;
  /** Forget any in-flight refresh (the host cleared or replaced the session itself). */
  reset: () => void;
  /**
   * Refresh ahead of expiry on a timer, retrying a transient failure with backoff for as long
   * as the refresh token is valid. Returns a stop function.
   */
  startAutoRefresh: () => () => void;
}

/** What `createSessionTokens` returns: a `SessionTokens` whose `forceRefresh` is always present. */
export interface ManagedSessionTokens extends SessionTokens {
  forceRefresh: (rejectedToken?: string) => Promise<string | null>;
  /**
   * Run a host operation that spends the held refresh token and returns the pair that replaces
   * it (a step-up, say), serialized with every refresh, so the two never spend one refresh
   * token between them. It first makes sure there is a usable access token (refreshing if due)
   * and waits out any refresh already running; while `spend` runs, a refresh anyone asks for
   * waits for it and shares the pair it yields. The pair is applied like a refreshed one.
   *
   * Resolves to the new access token. Rejects with `spend`'s error when it fails (the session
   * is kept, and refreshes then go ahead as usual), and with an Error when the session is
   * signed out, has ended, or was replaced while `spend` ran (its pair is then dropped).
   *
   * While `spend` runs, the held access token is handed out without waiting as long as it has
   * not expired, because `spend`'s own request asks for one. A `forceRefresh` naming that access
   * token as rejected (a 401, which may be `spend`'s own request) rejects with a 401 error and
   * keeps the session rather than waiting on `spend`; a later 401 recovers as usual.
   */
  exchange: (spend: (refreshToken: string) => Promise<TokenPair>) => Promise<string>;
}

const DEFAULT_SKEW_SECONDS = 30;
// setTimeout stores its delay in a signed 32-bit int; a longer one fires immediately.
const MAX_TIMER_MS = 2 ** 31 - 1;
// Auto-refresh retry backoff: 1 s doubling to 30 s, each scaled by a random 50-100 % so a
// fleet of displays that lost the network together does not come back in lockstep.
const RETRY_BASE_MS = 1_000;
const RETRY_CAP_MS = 30_000;
// While the access token is valid, one retry is aimed this far ahead of its expiry, not at it,
// so a request made then still has a token. No retry is armed within RETRY_BASE_MS before that
// aim: one that would be is moved onto it instead. After it, retries carry on at the backoff.
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
 * Exported within the package (not from index.ts): subscribe.ts reads the same predicate.
 */
export function isTransientRefreshError(err: unknown): boolean {
  if (!(err instanceof GraphQLRequestError)) return false;
  if (err.errors && err.errors.length > 0) return false;
  return err.status === 0 || err.status === 429 || err.status >= 500;
}

type Outcome =
  | { kind: 'ok'; token: string }
  | { kind: 'stale' } // the session was cleared or replaced while the refresh ran
  | { kind: 'ended' }
  | { kind: 'transient'; err: unknown }
  | { kind: 'declined'; err: unknown }; // an exchange's spend failed; it applied nothing

function expiryMs(token: string): number | undefined {
  const exp = decodeToken(token)?.exp;
  return exp === undefined ? undefined : exp * 1000;
}

export function createSessionTokens(opts: SessionTokensOptions): ManagedSessionTokens {
  const skew = opts.skewSeconds ?? DEFAULT_SKEW_SECONDS;
  const mode = opts.transientRefreshErrors;
  // Fail closed: a misspelt mode from an untyped caller must not quietly mean the default.
  if (mode !== undefined && mode !== 'null' && mode !== 'throw') {
    throw new TypeError(`createSessionTokens: transientRefreshErrors must be 'null' or 'throw', not ${String(mode)}`);
  }
  const throwTransient = mode === 'throw';
  let inFlight: Promise<Outcome> | null = null;
  // While an exchange's spend runs: the access token held when it started, which its own
  // request is sent with. Null otherwise.
  let exchanging: string | null = null;
  let lastRefresh: { from: string; pair: TokenPair } | null = null;

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

  // Spend `current`'s refresh token through `spend` (the host's refresh, or an exchange), as the
  // one in-flight use of it that every caller shares.
  const run = (
    current: TokenPair,
    spend: (refreshToken: string) => Promise<TokenPair>,
    isExchange: boolean,
  ): Promise<Outcome> => {
    // A spend that throws synchronously settles this before the assignment below, so it is
    // never left behind as the in-flight use; and a use reset() forgot never clears its successor.
    // An exchange marks `exchanging` before this runs: spend's own request asks for a token
    // synchronously, before the in-flight use is registered.
    let settled = false;
    let p: Promise<Outcome> | undefined = undefined;
    p = (async (): Promise<Outcome> => {
      try {
        const pair = await spend(current.refreshToken);
        // A promise cannot be cancelled, so guard here: if the live session is no
        // longer the one this refresh started from, drop the result.
        if (opts.read() !== current) return { kind: 'stale' };
        const next = { accessToken: pair.accessToken, refreshToken: pair.refreshToken };
        opts.apply(next);
        lastRefresh = { from: current.accessToken, pair: next };
        return { kind: 'ok', token: pair.accessToken };
      } catch (err) {
        // An exchange that failed is its caller's error, not a verdict on the session: a
        // wrong step-up code refuses the code, not the refresh token.
        if (isExchange) return { kind: 'declined', err };
        // Only end the session this refresh belonged to, not one since replaced.
        if (opts.read() !== current) return { kind: 'stale' };
        if (isTransientRefreshError(err)) return { kind: 'transient', err };
        end(err);
        return { kind: 'ended' };
      } finally {
        // Settled synchronously (p unset): nothing else can have started meanwhile.
        settled = true;
        if (p === undefined || inFlight === p) {
          inFlight = null;
          exchanging = null;
        }
      }
    })();
    if (!settled) inFlight = p;
    return p;
  };

  // One refresh of `current`, shared by every caller that asks while it runs. A caller that
  // finds an exchange running takes its pair; when the exchange failed it applied nothing, so
  // the held pair is refreshed as it would have been.
  const refreshShared = async (current: TokenPair): Promise<Outcome> => {
    for (;;) {
      const out = await (inFlight ?? run(current, opts.refresh, false));
      if (out.kind !== 'declined') return out;
      if (opts.read() !== current) return { kind: 'stale' };
    }
  };

  const getAccessToken = async (): Promise<string | null> => {
    const current = opts.read();
    if (!current) return null;
    if (isExpired(current.refreshToken, 0)) {
      end();
      return null;
    }
    if (!due(current.accessToken)) return current.accessToken;
    // An exchange is spending the refresh token, and its own request asks for an access token:
    // waiting on the exchange from inside it would never finish. The held token is still good.
    if (exchanging !== null && !isExpired(current.accessToken, 0)) return current.accessToken;

    const out = await refreshShared(current);
    if (out.kind === 'ok') return out.token;
    if (out.kind !== 'transient') return null;
    // A transient failure: the held token is still good if it has not actually expired.
    if (opts.read() !== current) return null;
    if (!isExpired(current.accessToken, 0)) return current.accessToken;
    // Nothing valid to hand over, but nothing was refused either: keep the session. Under
    // 'throw' the caller fails with the outage it hit (a null would send the request without a
    // token, and the server's 401 would read as a refusal); by default it gets v1.6's null.
    if (throwTransient) throw out.err;
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

      const backoff = Math.min(RETRY_CAP_MS, RETRY_BASE_MS * 2 ** failures) * (0.5 + Math.random() / 2);
      failures++;
      const left = (expiryMs(current.accessToken) ?? 0) - RETRY_EXPIRY_MARGIN_MS - Date.now();
      if (left > 0) {
        // A retry that would land within RETRY_BASE_MS of the aim before expiry (or past it) is
        // moved onto that aim. Otherwise a backoff that happens to land just short of the aim
        // leaves the aimed retry following it moments later: two requests back to back, against
        // a server that may have just answered 429.
        arm(left - backoff < RETRY_BASE_MS ? left : backoff);
        return;
      }
      // The access token has expired, but the refresh token may have hours left: keep trying
      // at the backoff. A retry is never armed past the refresh token's expiry; at it, fire()
      // finds the refresh token lapsed and ends the session.
      const refreshLeft = (expiryMs(current.refreshToken) ?? 0) - Date.now();
      arm(Math.min(backoff, refreshLeft));
    };

    schedule();
    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  };

  return {
    getAccessToken,
    forceRefresh: async (rejectedToken) => {
      const current = opts.read();
      if (!current) return null;
      // A late 401 must not refresh or end a replacement session.
      if (rejectedToken !== undefined && current.accessToken !== rejectedToken) {
        // A sibling request may have finished refreshing before this 401 arrived.
        return lastRefresh?.pair === current && lastRefresh.from === rejectedToken
          ? current.accessToken : null;
      }
      if (isExpired(current.refreshToken, 0)) {
        end();
        return null;
      }
      // The access token an exchange is being sent with was rejected. This 401 may be the
      // exchange's own request, which cannot wait for the exchange it is part of; and a refresh
      // now would spend the token the exchange is spending. Fail this request and keep the
      // session: once the exchange settles, a later 401 recovers as usual.
      if (exchanging !== null && rejectedToken === exchanging) {
        throw new GraphQLRequestError('Request failed (401)', 401);
      }
      const out = await refreshShared(current);
      if (out.kind === 'ok') return out.token;
      if (out.kind !== 'transient') return null;
      if (opts.read() !== current) return null;
      // The server rejected this access token, so it is not handed back; but a transient refresh
      // failure refused nothing, so the session is kept and the caller gets the outage it hit.
      throw out.err;
    },
    exchange: async (spend) => {
      // A usable access token first: spend's own request needs one, and a refresh it would
      // otherwise trigger mid-exchange could not run.
      if (!(await getAccessToken())) throw new Error('session unavailable');
      // Then wait out a refresh already spending the token (a forced one after a 401 starts
      // whatever the access token's expiry), and take the pair it leaves.
      while (inFlight) await inFlight;
      const current = opts.read();
      if (!current) throw new Error('session unavailable');
      if (isExpired(current.refreshToken, 0)) {
        end();
        throw new Error('session unavailable');
      }
      exchanging = current.accessToken;
      const out = await run(current, spend, true);
      if (out.kind === 'ok') return out.token;
      if (out.kind === 'declined') throw out.err;
      throw new Error('session changed');
    },
    reset: () => {
      inFlight = null;
      exchanging = null;
      lastRefresh = null;
    },
    startAutoRefresh,
  };
}
