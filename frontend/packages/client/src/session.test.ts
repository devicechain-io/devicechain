// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from 'vitest';

import { createSessionTokens, type SessionTokensOptions, type TokenPair } from './session';
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
function host(
  initial: TokenPair | null,
  refresh: (rt: string) => Promise<TokenPair>,
  extra: Pick<SessionTokensOptions, 'transientRefreshErrors'> = {},
) {
  const state = { pair: initial, ended: [] as unknown[] };
  const tokens = createSessionTokens({
    ...extra,
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

// The retry backoff is jittered with Math.random. Unpinned, it made a timing assertion pass or
// fail by the roll of the dice, so it is pinned (to the middle of its range) for every test, and
// the tests about the schedule choose their own rolls.
let random: MockInstance<() => number>;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  random = vi.spyOn(Math, 'random').mockReturnValue(0.5);
});
afterEach(() => {
  random.mockRestore();
  vi.useRealTimers();
});

describe('forceRefresh', () => {
  it('refreshes an unexpired rejected token and shares the exchange with concurrent callers', async () => {
    let release!: (p: TokenPair) => void;
    const refresh = vi.fn().mockReturnValue(new Promise<TokenPair>((r) => (release = r)));
    const { state, tokens } = host(pair(300), refresh);
    const rejected = state.pair!.accessToken;
    const calls = [tokens.forceRefresh(rejected), tokens.forceRefresh(rejected)];
    const next = pair(900);
    release(next);
    expect(await Promise.all(calls)).toEqual([next.accessToken, next.accessToken]);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(state.ended).toEqual([]);
    // A delayed 401 from a sibling request reuses that exchange rather than rotating again.
    expect(await tokens.forceRefresh(rejected)).toBe(next.accessToken);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it.each([
    ['a network failure', new GraphQLRequestError('offline', 0)],
    ['a 5xx', new GraphQLRequestError('Request failed (503)', 503)],
    ['a 429', new GraphQLRequestError('Request failed (429)', 429)],
  ])('keeps the session and throws on %s, never handing back the rejected token', async (_, err) => {
    const held = pair(300);
    const next = pair(900);
    const refresh = vi.fn().mockRejectedValueOnce(err).mockResolvedValueOnce(next);
    const { state, tokens } = host(held, refresh);
    await expect(tokens.forceRefresh(held.accessToken)).rejects.toBe(err);
    expect(state.ended).toEqual([]);
    expect(state.pair).toBe(held);
    // Nothing was refused, so the next 401 tries again with the same session.
    expect(await tokens.forceRefresh(held.accessToken)).toBe(next.accessToken);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(state.ended).toEqual([]);
  });

  // The negative control for the case above: the same call ends the session when the server
  // answered and refused, so the transient test is not passing because nothing ever ends.
  it.each([
    ['a GraphQL refusal', new GraphQLRequestError('refused', 200, [{ message: 'refused' }])],
    ['a 401', new GraphQLRequestError('Request failed (401)', 401)],
  ])('ends the session on %s and resolves null', async (_, err) => {
    const { state, tokens } = host(pair(300), vi.fn().mockRejectedValue(err));
    expect(await tokens.forceRefresh(state.pair!.accessToken)).toBeNull();
    expect(state.ended).toEqual([err]);
    expect(state.pair).toBeNull();
  });

  it('does not refresh a session that replaced the rejected token', async () => {
    const refresh = vi.fn();
    const { state, tokens } = host(pair(900), refresh);
    const current = state.pair;
    expect(await tokens.forceRefresh(jwt(300))).toBeNull();
    expect(refresh).not.toHaveBeenCalled();
    expect(state.pair).toBe(current);
    expect(state.ended).toEqual([]);
  });

  it('drops a forced refresh after sign-out', async () => {
    let release!: (p: TokenPair) => void;
    const { state, tokens } = host(pair(300), () => new Promise((r) => (release = r)));
    const call = tokens.forceRefresh();
    state.pair = null;
    tokens.reset();
    release(pair(900));
    expect(await call).toBeNull();
    expect(state.pair).toBeNull();
    expect(state.ended).toEqual([]);
  });
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

  // 🔴 The v1.6 contract of a published export: getAccessToken RESOLVES null here, never
  // rejects. A caller written against v1.6 checks for null; a rejection would reach it as an
  // unhandled one. What changed since v1.6 is only that the session is no longer ended.
  it.each([
    ['no response', offline()],
    ['a 503', httpError(503)],
    ['a 429', httpError(429)],
  ])('%s once the access token has expired resolves null by default, and keeps the session', async (_, err) => {
    const next = pair(900);
    const refresh = vi.fn().mockRejectedValueOnce(err).mockResolvedValueOnce(next);
    const { state, tokens } = host(pair(-1), refresh);
    const held = state.pair!;
    await expect(tokens.getAccessToken()).resolves.toBeNull();
    expect(state.ended).toEqual([]);
    expect(state.pair).toBe(held);
    // The refresh token is still good, so the next call refreshes with it.
    expect(await tokens.getAccessToken()).toBe(next.accessToken);
    expect(state.ended).toEqual([]);
  });

  it("an explicit 'null' is the default", async () => {
    const { state, tokens } = host(pair(-1), vi.fn().mockRejectedValue(offline()), { transientRefreshErrors: 'null' });
    await expect(tokens.getAccessToken()).resolves.toBeNull();
    expect(state.ended).toEqual([]);
  });

  // The negative control for the default: the opt-in really does reject, from the same session
  // state, so the null above is the mode's doing and not an accident of the fixture.
  it.each([
    ['no response', offline()],
    ['a 503', httpError(503)],
    ['a 429', httpError(429)],
  ])("%s once the access token has expired, under 'throw', fails the caller but keeps the session", async (_, err) => {
    const next = pair(900);
    const refresh = vi.fn().mockRejectedValueOnce(err).mockResolvedValueOnce(next);
    const { state, tokens } = host(pair(-1), refresh, { transientRefreshErrors: 'throw' });
    const held = state.pair!;
    // The caller cannot be given a valid token, so it gets the outage it hit, not a null that
    // would send its request unauthenticated.
    await expect(tokens.getAccessToken()).rejects.toBe(err);
    expect(state.ended).toEqual([]);
    expect(state.pair).toBe(held);
    // The refresh token is still good, so the next call refreshes with it.
    expect(await tokens.getAccessToken()).toBe(next.accessToken);
    expect(state.ended).toEqual([]);
  });

  it.each([
    ['by default', {}, 'null'],
    ["under 'throw'", { transientRefreshErrors: 'throw' as const }, 'throw'],
  ])('a session failing only transiently ends when its refresh token lapses, and not before (%s)', async (_, extra, mode) => {
    const refresh = vi.fn().mockRejectedValue(offline());
    const { state, tokens } = host(pair(-1, 60), refresh, extra);
    const failsTransiently = async () => {
      if (mode === 'throw') await expect(tokens.getAccessToken()).rejects.toBeInstanceOf(GraphQLRequestError);
      else await expect(tokens.getAccessToken()).resolves.toBeNull();
    };
    await failsTransiently();
    await vi.advanceTimersByTimeAsync(59_000);
    await failsTransiently();
    expect(state.ended).toEqual([]);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await tokens.getAccessToken()).toBeNull();
    expect(state.ended).toEqual([undefined]);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  // A refused refresh is the same in both modes: v1.6's null, and the session ends.
  it.each([
    ['by default', {}],
    ["under 'throw'", { transientRefreshErrors: 'throw' as const }],
  ])('a refused refresh of an expired token resolves null and ends the session (%s)', async (_, extra) => {
    const err = refused('UNAUTHENTICATED');
    const { state, tokens } = host(pair(-1), vi.fn().mockRejectedValue(err), extra);
    await expect(tokens.getAccessToken()).resolves.toBeNull();
    expect(state.ended).toEqual([err]);
  });

  // forceRefresh is new since v1.6 and rejects on a transient failure in both modes: a null from
  // it tells setUnauthorizedHandler the session ended.
  it('forceRefresh rejects on a transient failure even by default, and keeps the session', async () => {
    const err = offline();
    const { state, tokens } = host(pair(600), vi.fn().mockRejectedValue(err));
    const held = state.pair!;
    await expect(tokens.forceRefresh(held.accessToken)).rejects.toBe(err);
    expect(state.ended).toEqual([]);
    expect(state.pair).toBe(held);
  });

  it('refuses an unknown transientRefreshErrors mode instead of defaulting it', () => {
    expect(() =>
      createSessionTokens({
        read: () => null,
        apply: () => {},
        refresh: vi.fn(),
        onEnd: () => {},
        transientRefreshErrors: 'reject' as never,
      }),
    ).toThrow(TypeError);
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

  // Each case is a sequence of rolls and the attempt times (seconds after start) it must produce
  // against a token due at +30 s and expiring at +60 s. One retry is aimed at +59 s, a second
  // ahead of expiry, so a request then still has a token; a backoff that would land within a
  // second of that aim is moved onto it, so no two attempts ever come back to back. After it the
  // session is NOT ended: retries carry on at the backoff.
  it.each([
    ['the shortest backoffs', [0], [30, 30.5, 31.5, 33.5, 37.5, 45.5, 59]],
    ['the middle backoffs', [0.5], [30, 30.75, 32.25, 35.25, 41.25, 53.25, 59]],
    ['the longest backoffs', [1], [30, 31, 33, 37, 45, 59]],
    // A backoff from +45 s that would land at +58.5 s, half a second before the aim.
    ['a backoff landing just short of the final aim', [1, 1, 1, 1, 0.6875], [30, 31, 33, 37, 45, 59]],
    // ...but one landing a full second short of it is kept: that is far enough apart.
    ['a backoff landing a second short of the final aim', [1, 1, 1, 1, 0.625], [30, 31, 33, 37, 45, 58, 59]],
  ])('with %s, aims one retry just ahead of expiry and keeps the session past it', async (_, rolls, expected) => {
    const queue = [...rolls];
    random.mockImplementation(() => (queue.length > 1 ? queue.shift()! : queue[0]));
    const at: number[] = [];
    const refresh = vi.fn().mockImplementation(async () => {
      at.push((Date.now() - NOW) / 1000);
      throw offline();
    });
    const { state, tokens } = host(pair(60), refresh);
    const stop = tokens.startAutoRefresh();

    await vi.advanceTimersByTimeAsync(58_999); // the aim is still a millisecond away
    expect(at).toEqual(expected.slice(0, -1));
    await vi.advanceTimersByTimeAsync(1); // +59 s: the aimed attempt fails
    expect(at).toEqual(expected);
    expect(state.ended).toEqual([]);

    await vi.advanceTimersByTimeAsync(120_000); // and the attempts carry on past expiry
    expect(at.length).toBeGreaterThan(expected.length);
    expect(at.filter((s) => s > 60).length).toBeGreaterThan(0);
    expect(state.ended).toEqual([]);
    stop();
  });

  it('rides out an outage that outlasts the access token, while the refresh token is valid', async () => {
    const next = pair(900, 7200);
    let down = true;
    const at: number[] = [];
    const refresh = vi.fn().mockImplementation(async () => {
      at.push((Date.now() - NOW) / 1000);
      if (down) throw httpError(503);
      return next;
    });
    const { state, tokens } = host(pair(60, 3600), refresh); // access expires at +60 s
    const stop = tokens.startAutoRefresh();

    // Ten minutes of outage: ten access-token lifetimes.
    await vi.advanceTimersByTimeAsync(600_000);
    expect(state.ended).toEqual([]);
    // The backoff is capped: once it reaches the cap, attempts come no more often than every
    // 15 s (half the 30 s cap, the shortest jitter) and no less often than every 30 s.
    const late = at.filter((s) => s > 120);
    for (let i = 1; i < late.length; i++) {
      expect(late[i] - late[i - 1]).toBeGreaterThanOrEqual(15);
      expect(late[i] - late[i - 1]).toBeLessThanOrEqual(30);
    }

    down = false;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(state.pair).toEqual(next);
    expect(state.ended).toEqual([]);
    stop();
  });

  it('ends a transiently failing session when its refresh token lapses, and then stops', async () => {
    const at: number[] = [];
    const refresh = vi.fn().mockImplementation(async () => {
      at.push((Date.now() - NOW) / 1000);
      throw offline();
    });
    const { state, tokens } = host(pair(60, 300), refresh); // refresh token lapses at +300 s
    const stop = tokens.startAutoRefresh();

    await vi.advanceTimersByTimeAsync(299_999);
    expect(state.ended).toEqual([]);
    expect(at.every((s) => s < 300)).toBe(true);
    await vi.advanceTimersByTimeAsync(1);
    // Lapsed, not refused: onEnd gets no error, as for any lapsed refresh token.
    expect(state.ended).toEqual([undefined]);
    const attempts = at.length;
    await vi.advanceTimersByTimeAsync(600_000);
    expect(at.length).toBe(attempts);
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
