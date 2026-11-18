// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, it, expect, vi } from 'vitest';
import {
  areaPath, isIdentityArea, gql, GraphQLRequestError,
  setAuthTokenGetter, setIdentityTokenGetter, setUnauthorizedHandler,
  type Area, type TypedDocument,
} from './transport';
import { createSessionTokens, type TokenPair } from './session';

afterEach(() => {
  setAuthTokenGetter(null);
  setIdentityTokenGetter(null);
  setUnauthorizedHandler(null);
  vi.unstubAllGlobals();
});

const document = 'query Test($id: String!) { test(id: $id) }' as unknown as TypedDocument<
  { test: string }, { id: string }
>;

describe('HTTP 401 recovery', () => {
  function setup(statuses: number[]) {
    const fetcher = vi.fn(async () => new Response(
      JSON.stringify({ data: { test: 'ok' } }), { status: statuses.shift() ?? 500 },
    ));
    vi.stubGlobal('fetch', fetcher);
    setAuthTokenGetter(async () => 'old');
    setIdentityTokenGetter(async () => 'identity');
    const handler = { refresh: vi.fn(async () => 'fresh' as string | null), onEnd: vi.fn() };
    setUnauthorizedHandler(handler);
    return { fetcher, handler };
  }

  it('forces one refresh and replays exactly the same operation with the fresh bearer', async () => {
    const { fetcher, handler } = setup([401, 200]);
    expect(await gql('device-management', document, { id: 'one' })).toEqual({ test: 'ok' });
    expect(handler.refresh).toHaveBeenCalledExactlyOnceWith('old', false);
    expect(handler.onEnd).not.toHaveBeenCalled();
    expect(fetcher).toHaveBeenCalledTimes(2);
    const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
    expect(calls[0][0]).toBe(calls[1][0]);
    expect(calls[0][1].body).toBe(calls[1][1].body);
    expect(calls[0][1].headers).toMatchObject({ Authorization: 'Bearer old' });
    expect(calls[1][1].headers).toMatchObject({ Authorization: 'Bearer fresh' });
  });

  it('ends after a second 401 without looping, preserving the final HTTP error', async () => {
    const { fetcher, handler } = setup([401, 401]);
    await expect(gql('device-management', document, { id: 'one' })).rejects.toMatchObject({
      name: 'GraphQLRequestError', status: 401, message: 'Request failed (401)',
    });
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(handler.refresh).toHaveBeenCalledTimes(1);
    expect(handler.onEnd).toHaveBeenCalledExactlyOnceWith(expect.any(GraphQLRequestError), 'fresh', false);
  });

  it('ends without replay if refresh returns null', async () => {
    const { fetcher, handler } = setup([401]);
    handler.refresh.mockResolvedValue(null);
    await expect(gql('device-management', document, { id: 'one' })).rejects.toMatchObject({ status: 401 });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(handler.onEnd).toHaveBeenCalledExactlyOnceWith(expect.any(GraphQLRequestError), 'old', false);
  });

  it('fails with the refresh error and keeps the session if refresh throws', async () => {
    const { fetcher, handler } = setup([401]);
    const outage = new GraphQLRequestError('offline', 0);
    handler.refresh.mockRejectedValue(outage);
    await expect(gql('device-management', document, { id: 'one' })).rejects.toBe(outage);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(handler.onEnd).not.toHaveBeenCalled();
  });

  // The host wiring both apps use: refresh is session.forceRefresh, onEnd clears the session.
  describe('through session.forceRefresh', () => {
    function wired(refreshError: GraphQLRequestError) {
      const fetcher = vi.fn(async () => new Response('{}', { status: 401 }));
      vi.stubGlobal('fetch', fetcher);
      const state = { pair: { accessToken: 'old', refreshToken: `h.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 3600 }))}.s` } as TokenPair | null, ended: 0 };
      const session = createSessionTokens({
        read: () => state.pair,
        apply: (next) => { state.pair = next; },
        refresh: vi.fn().mockRejectedValue(refreshError),
        onEnd: () => { state.pair = null; state.ended++; },
      });
      setAuthTokenGetter(async () => state.pair?.accessToken ?? null);
      const onEnd = vi.fn((_e: GraphQLRequestError, token: string) => {
        if (state.pair?.accessToken === token) { state.pair = null; state.ended++; }
      });
      setUnauthorizedHandler({ refresh: (token) => session.forceRefresh(token), onEnd });
      return { fetcher, state, onEnd };
    }

    it('a transient refresh failure fails the request and keeps the session', async () => {
      const outage = new GraphQLRequestError('Request failed (503)', 503);
      const { fetcher, state, onEnd } = wired(outage);
      await expect(gql('device-management', document, { id: 'one' })).rejects.toBe(outage);
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(onEnd).not.toHaveBeenCalled();
      expect(state.ended).toBe(0);
      expect(state.pair?.accessToken).toBe('old');
    });

    // Negative control: the same wiring DOES end the session when the server refuses the refresh.
    it('a refused refresh ends the session', async () => {
      const refused = new GraphQLRequestError('invalid refresh token', 200, [{ message: 'invalid refresh token' }]);
      const { state, onEnd } = wired(refused);
      await expect(gql('device-management', document, { id: 'one' })).rejects.toMatchObject({ status: 401 });
      expect(state.ended).toBe(1);
      expect(state.pair).toBeNull();
      expect(onEnd).toHaveBeenCalledTimes(1);
    });
  });

  it('reports the identity lane and never substitutes the tenant bearer', async () => {
    const { handler } = setup([401]);
    handler.refresh.mockResolvedValue(null);
    await expect(gql('user-management/admin', document, { id: 'one' })).rejects.toMatchObject({ status: 401 });
    expect(handler.refresh).toHaveBeenCalledExactlyOnceWith('identity', true);
    expect(handler.onEnd).toHaveBeenCalledExactlyOnceWith(expect.any(GraphQLRequestError), 'identity', true);
  });

  it.each([401, 403, 500])('does not recover anonymous HTTP %s', async (status) => {
    const { fetcher, handler } = setup([status]);
    await expect(gql('user-management', document, { id: 'one' }, { anonymous: true })).rejects.toMatchObject({ status });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(handler.refresh).not.toHaveBeenCalled();
    expect(handler.onEnd).not.toHaveBeenCalled();
  });

  it('preserves behavior for hosts that do not opt in', async () => {
    const { fetcher } = setup([401]);
    setUnauthorizedHandler(null);
    await expect(gql('device-management', document, { id: 'one' })).rejects.toMatchObject({
      name: 'GraphQLRequestError', status: 401, message: 'Request failed (401)',
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each([403, 500])('does not refresh authenticated HTTP %s', async (status) => {
    const { handler } = setup([status]);
    await expect(gql('device-management', document, { id: 'one' })).rejects.toMatchObject({ status });
    expect(handler.refresh).not.toHaveBeenCalled();
    expect(handler.onEnd).not.toHaveBeenCalled();
  });
});

describe('areaPath', () => {
  it('routes an area to its ingress path', () => {
    expect(areaPath('device-management')).toBe('/api/device-management/graphql');
  });

  it('carries an admin area through as a second path on the same service', () => {
    // The ingress matches /api/<area-segment>(/|$)(.*) and rewrites to $2, so the
    // "/admin" here lands as /admin/graphql on the user-management Service. Nothing
    // in the ingress knows the admin plane exists — this is why adding one costs no
    // deploy change.
    expect(areaPath('user-management/admin')).toBe('/api/user-management/admin/graphql');
    expect(areaPath('ai-inference/admin')).toBe('/api/ai-inference/admin/graphql');
  });
});

describe('isIdentityArea', () => {
  // This is a security default, not a convenience. An /admin endpoint accepts ONLY
  // an identity token (ADR-033), so sending a tenant access token there is never
  // right. Deriving the lane from the area is what stops a new admin call from
  // silently authenticating with the wrong credential — the mistake is easy to make
  // because the area string and the token choice used to be independent, and it
  // fails as a 401 far from its cause.
  it('puts every admin and settings lane on the identity token', () => {
    const identityAreas: Area[] = [
      'user-management/admin',
      'user-management/settings',
      'ai-inference/admin',
    ];
    for (const area of identityAreas) {
      expect(isIdentityArea(area), `${area} must authenticate with the identity token`).toBe(true);
    }
  });

  it('leaves every tenant data plane on the tenant access token', () => {
    const tenantAreas: Area[] = [
      'user-management',
      'device-management',
      'event-management',
      'event-processing',
      'device-state',
      'command-delivery',
      'dashboard-management',
      'outbound-connectors',
      'notification-management',
    ];
    for (const area of tenantAreas) {
      expect(isIdentityArea(area), `${area} must authenticate with the tenant access token`).toBe(
        false,
      );
    }
  });

  it('keys off the lane suffix, not the service name', () => {
    // user-management serves BOTH lanes, so the service name cannot decide this —
    // only the area's suffix can.
    expect(isIdentityArea('user-management')).toBe(false);
    expect(isIdentityArea('user-management/admin')).toBe(true);
  });
});
