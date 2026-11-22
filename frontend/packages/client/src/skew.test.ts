// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { gql, setAuthTokenGetter, setUnauthorizedHandler, type TypedDocument } from './transport';
import {
  disableSchemaNegotiation,
  enableSchemaNegotiation,
  isSchemaSkewError,
  resetSchemaNegotiation,
  SchemaSkewError,
} from './skew';

// What an OLDER instance serves: no `serial` on Device, no `serial` on the create input.
const OLD_SDL = `
  type Query { device(token: String!): Device }
  type Mutation { createDevice(input: DeviceInput!): Device }
  type Device { token: String! name: String }
  input DeviceInput { token: String! name: String }
`;
const NEW_SDL = OLD_SDL.replace('name: String }\n  input', 'name: String serial: String }\n  input').replace(
  'input DeviceInput { token: String! name: String }',
  'input DeviceInput { token: String! name: String serial: String }',
);

type Doc<R, V> = TypedDocument<R, V>;
const readOk = 'query D($t: String!) { device(token: $t) { token name } }' as unknown as Doc<unknown, { t: string }>;
// A NEWER console's documents: they select / send `serial`.
const readSerial = 'query D($t: String!) { device(token: $t) { token serial } }' as unknown as Doc<unknown, { t: string }>;
const create = 'mutation C($i: DeviceInput!) { createDevice(input: $i) { token } }' as unknown as Doc<
  unknown,
  { i: { token: string; serial?: string } }
>;

function instance(opts: { sdl?: string; discovery?: number | 'manifest'; format?: number }) {
  const calls: { method: string; body?: string }[] = [];
  const fetcher = vi.fn(async (_url: string, init: RequestInit) => {
    calls.push({ method: String(init.method), body: init.body as string | undefined });
    if (init.method === 'OPTIONS') {
      if (opts.discovery !== undefined && opts.discovery !== 'manifest') {
        return new Response(opts.discovery === 204 ? null : '', { status: opts.discovery });
      }
      return new Response(JSON.stringify({ format: opts.format ?? 1, schema: opts.sdl }), { status: 200 });
    }
    return new Response(JSON.stringify({ data: { ok: true } }), { status: 200 });
  });
  vi.stubGlobal('fetch', fetcher);
  return { calls, fetcher, posts: () => calls.filter((c) => c.method === 'POST').length };
}

const failure = (p: Promise<unknown>) =>
  p.then(
    () => { throw new Error('expected the request to be refused'); },
    (e: unknown) => e as SchemaSkewError,
  );

beforeEach(() => {
  setAuthTokenGetter(async () => 'tok');
  enableSchemaNegotiation();
});

afterEach(() => {
  disableSchemaNegotiation();
  setAuthTokenGetter(null);
  setUnauthorizedHandler(null);
  vi.unstubAllGlobals();
});

describe('a newer console against an older instance', () => {
  it('refuses a selection the instance does not serve, and sends nothing', async () => {
    const inst = instance({ sdl: OLD_SDL });
    const err = await failure(gql('device-management', readSerial, { t: 'a' }));
    expect(isSchemaSkewError(err)).toBe(true);
    expect(err).toBeInstanceOf(SchemaSkewError);
    expect(err).toMatchObject({ kind: 'unsupported', status: 0, endpoint: '/api/device-management/graphql' });
    expect(err.message).toContain('serial');
    expect(err.message).toContain('v1.9.0');
    expect(inst.posts()).toBe(0);
  });

  it('refuses a nested input field the instance lacks, rather than dropping it', async () => {
    const inst = instance({ sdl: OLD_SDL });
    await expect(
      gql('device-management', create, { i: { token: 'a', serial: 'S1' } }),
    ).rejects.toMatchObject({ kind: 'unsupported', message: expect.stringContaining('serial') });
    expect(inst.posts()).toBe(0);
  });

  it('sends an operation the older instance does serve, unmodified', async () => {
    const inst = instance({ sdl: OLD_SDL });
    await gql('device-management', readOk, { t: 'a' });
    const post = inst.calls.find((c) => c.method === 'POST');
    expect(JSON.parse(post!.body!)).toEqual({ query: readOk.toString(), variables: { t: 'a' } });
  });

  it('serves the same newer operations against an instance that has the fields', async () => {
    const inst = instance({ sdl: NEW_SDL });
    await gql('device-management', readSerial, { t: 'a' });
    await gql('device-management', create, { i: { token: 'a', serial: 'S1' } });
    expect(inst.posts()).toBe(2);
  });

  it('discovers once per endpoint and carries the bearer on an OPTIONS with the schema Accept', async () => {
    const inst = instance({ sdl: NEW_SDL });
    await Promise.all([gql('device-management', readOk, { t: 'a' }), gql('device-management', readOk, { t: 'b' })]);
    await gql('device-management', readOk, { t: 'c' });
    expect(inst.calls.filter((c) => c.method === 'OPTIONS')).toHaveLength(1);
    const first = inst.fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(first[1]).toMatchObject({
      method: 'OPTIONS',
      redirect: 'error',
      headers: { Authorization: 'Bearer tok', Accept: 'application/vnd.devicechain.schema+json' },
    });
  });
});

describe('an instance whose version cannot be identified', () => {
  it.each([400, 404, 405, 204])('fails loudly on discovery status %i and sends nothing', async (status) => {
    const inst = instance({ discovery: status });
    const err = await failure(gql('device-management', readOk, { t: 'a' }));
    expect(err).toBeInstanceOf(SchemaSkewError);
    expect(err).toMatchObject({ kind: 'undiscoverable' });
    expect(err.message).toContain('v1.9.0');
    expect(inst.posts()).toBe(0);
  });

  it('fails loudly on an unknown discovery format', async () => {
    const inst = instance({ sdl: NEW_SDL, format: 2 });
    await expect(gql('device-management', readOk, { t: 'a' })).rejects.toMatchObject({ kind: 'invalid-contract' });
    expect(inst.posts()).toBe(0);
  });

  it('fails loudly on an unparseable schema', async () => {
    const inst = instance({ sdl: 'type {{{' });
    await expect(gql('device-management', readOk, { t: 'a' })).rejects.toMatchObject({ kind: 'invalid-contract' });
    expect(inst.posts()).toBe(0);
  });

  it('does not cache a failed discovery: the next request asks again and can succeed', async () => {
    instance({ discovery: 404 });
    await expect(gql('device-management', readOk, { t: 'a' })).rejects.toBeInstanceOf(SchemaSkewError);
    const inst = instance({ sdl: NEW_SDL });
    await gql('device-management', readOk, { t: 'a' });
    expect(inst.posts()).toBe(1);
  });

  it('an outage during discovery is a plain request error, not a skew verdict', async () => {
    instance({ discovery: 503 });
    const err = await failure(gql('device-management', readOk, { t: 'a' }));
    expect(isSchemaSkewError(err)).toBe(false);
    expect(err).toMatchObject({ status: 503 });
  });
});

describe('scope of negotiation', () => {
  it('never checks anonymous exchanges', async () => {
    const inst = instance({ discovery: 404 });
    await gql('user-management', readOk, { t: 'a' }, { anonymous: true });
    expect(inst.calls.map((c) => c.method)).toEqual(['POST']);
  });

  it('does nothing unless enabled', async () => {
    disableSchemaNegotiation();
    const inst = instance({ discovery: 404 });
    await gql('device-management', readOk, { t: 'a' });
    expect(inst.calls.map((c) => c.method)).toEqual(['POST']);
  });

  it('resetSchemaNegotiation rediscovers after an upgrade', async () => {
    const inst = instance({ sdl: OLD_SDL });
    await gql('device-management', readOk, { t: 'a' });
    resetSchemaNegotiation();
    await gql('device-management', readOk, { t: 'a' });
    expect(inst.calls.filter((c) => c.method === 'OPTIONS')).toHaveLength(2);
  });

  it('discovery rides the 401 recovery: one refresh, replayed with the fresh bearer', async () => {
    let first = true;
    const fetcher = vi.fn(async (_u: string, init: RequestInit) => {
      if (init.method === 'OPTIONS') {
        if (first) {
          first = false;
          return new Response('', { status: 401 });
        }
        return new Response(JSON.stringify({ format: 1, schema: NEW_SDL }), { status: 200 });
      }
      return new Response(JSON.stringify({ data: { ok: true } }), { status: 200 });
    });
    vi.stubGlobal('fetch', fetcher);
    const handler = { refresh: vi.fn(async () => 'fresh' as string | null), onEnd: vi.fn() };
    setUnauthorizedHandler(handler);
    await gql('device-management', readOk, { t: 'a' });
    expect(handler.refresh).toHaveBeenCalledExactlyOnceWith('tok', false);
    const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
    expect(calls[1][1].headers).toMatchObject({ Authorization: 'Bearer fresh' });
    expect(calls[2][1].headers).toMatchObject({ Authorization: 'Bearer fresh' });
  });
});
