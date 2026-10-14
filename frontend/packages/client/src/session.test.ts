// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createSessionTokens, type TokenPair } from './session';

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
