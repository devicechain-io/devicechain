// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The "never refuses its own instance" guard for schema negotiation, for the dashboard viewer's
// own documents (queries.ts; the widget documents it renders are guarded in @devicechain/dashboards).
// Negotiation refuses any operation the connected instance's schema cannot serve, so a viewer
// document that does not validate against the SDL its own release serves would be refused by a
// matching instance. Each document is sent through the real client with negotiation on, against
// the SDL of the service that serves it, and must reach the wire.
//
// The anonymous documents (sign-in, refresh, sign-out) are never negotiated in the app, but they
// are held to their service's SDL here all the same: a document that drifted from the schema
// would fail sign-in itself.

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  disableSchemaNegotiation,
  enableSchemaNegotiation,
  gql,
  resetSchemaNegotiation,
  setAuthTokenGetter,
  type TypedDocument,
} from '@devicechain/client';

import * as queries from './queries';

const sdl = (service: string) =>
  readFileSync(path.resolve(__dirname, '../../../../backend/services', service, 'graphql/schema.graphql'), 'utf8');

// The service that serves each document. Every operation exported by queries.ts must appear here,
// so a new document cannot be added without naming where it is served (and being checked there).
const servedBy: Record<string, string> = {
  LOGIN: 'user-management',
  VERIFY_MFA: 'user-management',
  SELECT_TENANT: 'user-management',
  REFRESH: 'user-management',
  BEGIN_SSO: 'user-management',
  COMPLETE_SSO: 'user-management',
  LOGOUT: 'user-management',
  TENANT_BASEMAP: 'user-management',
  PUBLISHED_DASHBOARD: 'dashboard-management',
  DASHBOARDS: 'dashboard-management',
};

const isOperation = (v: unknown): v is string => typeof v === 'string' && /^\s*(query|mutation|subscription)\b/.test(v);
const operations = Object.entries(queries).filter(([, v]) => isOperation(v)) as [string, string][];

function serve(schema: string): string[] {
  const methods: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init: RequestInit) => {
      methods.push(init.method as string);
      if (init.method === 'OPTIONS') return new Response(JSON.stringify({ format: 1, schema }), { status: 200 });
      return new Response(JSON.stringify({ data: {} }), { status: 200 });
    }),
  );
  return methods;
}

beforeEach(() => {
  setAuthTokenGetter(async () => 'tok');
  enableSchemaNegotiation();
});

afterEach(() => {
  disableSchemaNegotiation();
  setAuthTokenGetter(null);
  vi.unstubAllGlobals();
});

describe('every dashboard viewer document is served by its own service', () => {
  it('names the service for every operation queries.ts exports, and no other', () => {
    expect(operations.map(([name]) => name).sort()).toEqual(Object.keys(servedBy).sort());
    // A floor, so an empty enumeration cannot pass.
    expect(operations.length).toBeGreaterThanOrEqual(10);
  });

  for (const [name, doc] of operations) {
    const service = servedBy[name];
    it(`${name} passes negotiation against ${service}`, async () => {
      const methods = serve(sdl(service));
      resetSchemaNegotiation();
      await gql('user-management', doc as unknown as TypedDocument<unknown, Record<string, never>>);
      expect(methods).toEqual(['OPTIONS', 'POST']);
    });
  }

  // Negative controls: the same harness refuses a document the schema does not serve, and refuses
  // a real one against the WRONG service's schema, so the loop above cannot pass by never checking.
  it('refuses a document the schema does not serve', async () => {
    const methods = serve(sdl('user-management'));
    resetSchemaNegotiation();
    const bogus = 'query { thisFieldDoesNotExist }' as unknown as TypedDocument<unknown, Record<string, never>>;
    await expect(gql('user-management', bogus)).rejects.toMatchObject({ kind: 'unsupported' });
    expect(methods).toEqual(['OPTIONS']);
  });

  it('refuses a published-dashboard query against a schema that lacks it', async () => {
    const methods = serve(sdl('user-management'));
    resetSchemaNegotiation();
    await expect(gql('user-management', queries.PUBLISHED_DASHBOARD, { token: 't' })).rejects.toMatchObject({
      kind: 'unsupported',
    });
    expect(methods).toEqual(['OPTIONS']);
  });
});
