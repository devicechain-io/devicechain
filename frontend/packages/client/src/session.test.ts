// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createSessionTokens, type TokenPair } from './session';
import { GraphQLRequestError } from './transport';

const NOW = Date.UTC(2026, 9, 14, 12, 0, 0);

// An unsigned token whose only meaningful claim is `exp` (the client never verifies).
function jwt(expSecondsFromNow: number): string {
  const payload = btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + expSecondsFromNow }));
  return `h.${payload}.s`;
}

function pair(accessIn: number, refreshIn = 3600): TokenPair {
  return { accessToken: jwt(accessIn), refreshToken: jwt(refreshIn) };
}

// A host that keeps the pair in a variable, like both real hosts keep it in a ref.
function host(initial: TokenPair | null, refresh: (rt: string) => Promise<TokenPair>) {
  const state = { pair: initial, ended: [] as unknown[] };
  const tokens = createSessionTokens({
    read: () => state.pair,
    apply: (next) => {
      state.pair = next;
    },
    refresh,
    onEnd: (err) => {
      state.pair = null;
      state.ended.push(err);
    },
  });
  return { state, tokens };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});
afterEach(() => {
  vi.useRealTimers();
});

describe('getAccessToken', () => {
  it('returns the held token without refreshing while it is outside the skew', async () => {
    const refresh = vi.fn();
    const { state, tokens } = host(pair(300), refresh);
    expect(await tokens.getAccessToken()).toBe(state.pair!.accessToken);
    expect(refresh).not.toHaveBeenCalled();
  });

  it('refreshes once the token is inside the skew, and holds the new pair', async () => {
    const next = pair(900);
    const refresh = vi.fn().mockResolvedValue(next);
    const { state, tokens } = host(pair(10), refresh);
    const old = state.pair!;
    expect(await tokens.getAccessToken()).toBe(next.accessToken);
    expect(refresh).toHaveBeenCalledWith(old.refreshToken);
    expect(state.pair).toEqual(next);
  });

  it('shares ONE refresh between concurrent callers', async () => {
    let release!: (p: TokenPair) => void;
    const refresh = vi.fn().mockReturnValue(new Promise<TokenPair>((r) => (release = r)));
    const { tokens } = host(pair(10), refresh);
    const calls = [tokens.getAccessToken(), tokens.getAccessToken(), tokens.getAccessToken()];
    const next = pair(900);
    release(next);
    expect(await Promise.all(calls)).toEqual([next.accessToken, next.accessToken, next.accessToken]);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('ends the session, returning null, when the refresh fails', async () => {
    const boom = new Error('refused');
    const { state, tokens } = host(pair(10), vi.fn().mockRejectedValue(boom));
    expect(await tokens.getAccessToken()).toBeNull();
    expect(state.ended).toEqual([boom]);
  });

  it('ends the session without calling refresh when the refresh token has lapsed', async () => {
    const refresh = vi.fn();
    const { state, tokens } = host(pair(10, -5), refresh);
    expect(await tokens.getAccessToken()).toBeNull();
    expect(refresh).not.toHaveBeenCalled();
    expect(state.ended).toEqual([undefined]);
  });

  it('drops a refresh that lands after the session was replaced', async () => {
    let release!: (p: TokenPair) => void;
    const refresh = vi.fn().mockReturnValue(new Promise<TokenPair>((r) => (release = r)));
    const { state, tokens } = host(pair(10), refresh);
    const call = tokens.getAccessToken();
    const replacement = pair(900);
    state.pair = replacement; // signed in again while the refresh was in flight
    release(pair(900));
    expect(await call).toBeNull();
    expect(state.pair).toBe(replacement);
    expect(state.ended).toEqual([]);
  });

  it('does not end a session that replaced the one a failing refresh belonged to', async () => {
    let fail!: (e: unknown) => void;
    const refresh = vi.fn().mockReturnValue(new Promise<TokenPair>((_, rej) => (fail = rej)));
    const { state, tokens } = host(pair(10), refresh);
    const call = tokens.getAccessToken();
    const replacement = pair(900);
    state.pair = replacement;
    fail(new Error('late'));
    expect(await call).toBeNull();
    expect(state.pair).toBe(replacement);
    expect(state.ended).toEqual([]);
  });

  it('returns null when signed out', async () => {
    const { tokens } = host(null, vi.fn());
    expect(await tokens.getAccessToken()).toBeNull();
  });
});

describe('startAutoRefresh', () => {
  it('refreshes BEFORE expiry, by the skew, without any caller asking', async () => {
    const next = pair(900);
    const refresh = vi.fn().mockResolvedValue(next);
    const { state, tokens } = host(pair(120), refresh); // default skew 30 s: due at +90 s
    const stop = tokens.startAutoRefresh();

    await vi.advanceTimersByTimeAsync(89_000);
    expect(refresh).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(2_000); // +91 s, still 29 s short of expiry
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(state.pair).toEqual(next);
    stop();
  });

  it('re-arms after a successful refresh', async () => {
    const refresh = vi.fn().mockImplementation(async () => pair(100));
    const { tokens } = host(pair(60), refresh);
    const stop = tokens.startAutoRefresh();
    await vi.advanceTimersByTimeAsync(31_000); // first due at +30 s
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(71_000); // new token expires in 100 s: due 70 s later
    expect(refresh).toHaveBeenCalledTimes(2);
    stop();
  });

  it('ends the session when the scheduled refresh fails, and stops re-arming', async () => {
    const refresh = vi.fn().mockRejectedValue(new Error('refused'));
    const { state, tokens } = host(pair(60), refresh);
    tokens.startAutoRefresh();
    await vi.advanceTimersByTimeAsync(31_000);
    expect(state.ended).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(600_000);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('does nothing after stop()', async () => {
    const refresh = vi.fn().mockResolvedValue(pair(900));
    const { tokens } = host(pair(60), refresh);
    tokens.startAutoRefresh()();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(refresh).not.toHaveBeenCalled();
  });
});

// What the transport throws: no response at all, an HTTP error status, or a GraphQL error.
const offline = () => new GraphQLRequestError('Failed to fetch', 0);
const httpError = (status: number) => new GraphQLRequestError(`Request failed (${status})`, status);
const refused = (code: string) =>
  new GraphQLRequestError('refused', 200, [{ message: 'refused', extensions: { code } }]);

describe('transient vs definitive refresh failures', () => {
  it.each([
    ['no response', offline()],
    ['a 503', httpError(503)],
    ['a 429', httpError(429)],
  ])('%s keeps a still-valid access token in use, without ending the session', async (_, err) => {
    const { state, tokens } = host(pair(10), vi.fn().mockRejectedValue(err));
    const held = state.pair!;
    expect(await tokens.getAccessToken()).toBe(held.accessToken);
    expect(state.ended).toEqual([]);
    expect(state.pair).toBe(held);
  });

  it('a transient failure once the access token has actually expired ends the session', async () => {
    const err = offline();
    const { state, tokens } = host(pair(-1), vi.fn().mockRejectedValue(err));
    expect(await tokens.getAccessToken()).toBeNull();
    expect(state.ended).toEqual([err]);
  });

  it.each([
    ['a 401', httpError(401)],
    ['a 400', httpError(400)],
    ['an invalid refresh token', refused('UNAUTHENTICATED')],
    ['SSO_REQUIRED', refused('SSO_REQUIRED')],
    ['a GraphQL error carried on a 500', new GraphQLRequestError('x', 500, [{ message: 'x' }])],
  ])('%s is definitive: the session ends at once', async (_, err) => {
    const { state, tokens } = host(pair(10), vi.fn().mockRejectedValue(err));
    expect(await tokens.getAccessToken()).toBeNull();
    expect(state.ended).toEqual([err]);
  });
});

describe('startAutoRefresh retries', () => {
  it('rides out a network blip: retries with backoff and keeps the session', async () => {
    const next = pair(900);
    const refresh = vi
      .fn()
      .mockRejectedValueOnce(offline())
      .mockRejectedValueOnce(httpError(502))
      .mockResolvedValueOnce(next)
      .mockImplementation(async () => pair(900));
    const { state, tokens } = host(pair(60), refresh); // due at +30 s, expires at +60 s
    const stop = tokens.startAutoRefresh();

    await vi.advanceTimersByTimeAsync(30_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(state.ended).toEqual([]);

    // Backoff is 1 s then 2 s, each scaled by 50-100 %: both retries land within 3 s.
    await vi.advanceTimersByTimeAsync(3_000);
    expect(refresh).toHaveBeenCalledTimes(3);
    expect(state.pair).toEqual(next);
    expect(state.ended).toEqual([]);

    // Re-armed against the new token (minted at +0 s, expiring at +900 s): due at +870 s.
    await vi.advanceTimersByTimeAsync(836_000); // +869 s
    expect(refresh).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(refresh).toHaveBeenCalledTimes(4);
    stop();
  });

  it('gives up only when the access token actually expires, not before', async () => {
    const refresh = vi.fn().mockRejectedValue(offline());
    const { state, tokens } = host(pair(60), refresh);
    const stop = tokens.startAutoRefresh();

    await vi.advanceTimersByTimeAsync(58_000); // 2 s of life left
    expect(state.ended).toEqual([]);
    const attempts = refresh.mock.calls.length;
    expect(attempts).toBeGreaterThan(2);

    await vi.advanceTimersByTimeAsync(2_000);
    expect(state.ended).toHaveLength(1);
    expect(state.ended[0]).toBeInstanceOf(GraphQLRequestError);

    await vi.advanceTimersByTimeAsync(600_000);
    expect(refresh.mock.calls.length).toBeLessThanOrEqual(attempts + 1); // and then stops
    stop();
  });

  it('a refusal during auto-refresh ends the session at once, with no retry', async () => {
    const err = refused('UNAUTHENTICATED');
    const refresh = vi.fn().mockRejectedValue(err);
    const { state, tokens } = host(pair(60), refresh);
    tokens.startAutoRefresh();
    await vi.advanceTimersByTimeAsync(31_000);
    expect(state.ended).toEqual([err]);
    await vi.advanceTimersByTimeAsync(600_000);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('stop() also cancels a pending retry', async () => {
    const refresh = vi.fn().mockRejectedValue(offline());
    const { state, tokens } = host(pair(60), refresh);
    const stop = tokens.startAutoRefresh();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    stop();
    await vi.advanceTimersByTimeAsync(600_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(state.ended).toEqual([]);
  });
});

// An unsigned token carrying both `exp` and `iat` (a lifetime the device clock cannot skew).
function jwtWithIat(lifetimeSeconds: number): string {
  const now = Math.floor(Date.now() / 1000);
  return `h.${btoa(JSON.stringify({ iat: now, exp: now + lifetimeSeconds }))}.s`;
}

describe('the skew against short-lived tokens', () => {
  it('is capped at half the token lifetime, so a fresh short-lived token is not already due', async () => {
    // 120 s skew against a 60 s token would make every token due the moment it is issued.
    const refresh = vi.fn().mockImplementation(async () => ({ accessToken: jwtWithIat(60), refreshToken: jwt(3600) }));
    const state = { pair: { accessToken: jwtWithIat(60), refreshToken: jwt(3600) } as TokenPair | null };
    const tokens = createSessionTokens({
      read: () => state.pair,
      apply: (next) => {
        state.pair = next;
      },
      refresh,
      onEnd: () => {},
      skewSeconds: 120,
    });
    expect(await tokens.getAccessToken()).toBe(state.pair!.accessToken);
    expect(refresh).not.toHaveBeenCalled();

    const stop = tokens.startAutoRefresh();
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    // Due at half-life: about one refresh every 30 s, not a loop.
    expect(refresh.mock.calls.length).toBeGreaterThanOrEqual(15);
    expect(refresh.mock.calls.length).toBeLessThanOrEqual(25);
    stop();
  });

  it('never refreshes in a loop, even when every token it gets back already looks due', async () => {
    // A device clock far ahead of the server's: each new token looks expired on arrival.
    const refresh = vi.fn().mockImplementation(async () => pair(-60, 3600));
    const { tokens } = host(pair(-60, 3600), refresh);
    const stop = tokens.startAutoRefresh();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(refresh.mock.calls.length).toBeLessThanOrEqual(7); // the 10 s floor
    stop();
  });
});
