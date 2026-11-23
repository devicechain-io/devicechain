// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  disableSchemaNegotiation,
  gql,
  resetSchemaNegotiation,
  setAuthTokenGetter,
  type TypedDocument,
} from '@devicechain/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TENANT_BASEMAP } from './queries';
import { configureSchemaNegotiation, schemaNegotiationWanted } from './schema-negotiation';

describe('when the dashboard viewer negotiates its schema', () => {
  it('is off in a production build served by its own instance', () => {
    expect(schemaNegotiationWanted({ DEV: false })).toBe(false);
    expect(schemaNegotiationWanted({})).toBe(false);
  });

  it('is on under vite dev, where the proxy can reach any instance', () => {
    expect(schemaNegotiationWanted({ DEV: true })).toBe(true);
  });

  it('is on in a build made for a viewer hosted apart from its instance', () => {
    expect(schemaNegotiationWanted({ DEV: false, VITE_SCHEMA_NEGOTIATION: 'on' })).toBe(true);
    expect(schemaNegotiationWanted({ DEV: false, VITE_SCHEMA_NEGOTIATION: ' TRUE ' })).toBe(true);
  });

  it('can be switched off even in dev', () => {
    expect(schemaNegotiationWanted({ DEV: true, VITE_SCHEMA_NEGOTIATION: 'off' })).toBe(false);
  });

  it('ignores an unrecognised setting rather than guessing', () => {
    expect(schemaNegotiationWanted({ DEV: false, VITE_SCHEMA_NEGOTIATION: 'maybe' })).toBe(false);
    expect(schemaNegotiationWanted({ DEV: true, VITE_SCHEMA_NEGOTIATION: 'maybe' })).toBe(true);
  });
});

describe('configuring negotiation at start-up', () => {
  it('enables it where it is wanted and not where it is not', () => {
    const enable = vi.fn();
    expect(configureSchemaNegotiation({ DEV: false }, enable)).toBe(false);
    expect(enable).not.toHaveBeenCalled();
    expect(configureSchemaNegotiation({ DEV: true }, enable)).toBe(true);
    expect(configureSchemaNegotiation({ DEV: false, VITE_SCHEMA_NEGOTIATION: 'on' }, enable)).toBe(true);
    expect(enable).toHaveBeenCalledTimes(2);
  });

  // The real SDK, not the seam: an authenticated request sends a discovery OPTIONS first only
  // once the viewer has turned negotiation on, and sends none while it has not.
  describe('against the real client', () => {
    beforeEach(() => setAuthTokenGetter(async () => 'tok'));
    afterEach(() => {
      disableSchemaNegotiation();
      setAuthTokenGetter(null);
      vi.unstubAllGlobals();
    });

    const doc = TENANT_BASEMAP as unknown as TypedDocument<unknown, Record<string, never>>;
    function stubFetch(): string[] {
      const methods: string[] = [];
      vi.stubGlobal(
        'fetch',
        vi.fn(async (_url: string, init: RequestInit) => {
          methods.push(init.method as string);
          if (init.method === 'OPTIONS') {
            return new Response(JSON.stringify({ format: 1, schema: 'type Query { tenant: String }' }), { status: 200 });
          }
          return new Response(JSON.stringify({ data: {} }), { status: 200 });
        }),
      );
      return methods;
    }

    it('sends no discovery request in a production build', async () => {
      const methods = stubFetch();
      resetSchemaNegotiation();
      configureSchemaNegotiation({ DEV: false });
      await gql('user-management', doc).catch(() => undefined);
      expect(methods).toEqual(['POST']);
    });

    it('discovers the schema first when the build asks for it', async () => {
      const methods = stubFetch();
      resetSchemaNegotiation();
      configureSchemaNegotiation({ DEV: false, VITE_SCHEMA_NEGOTIATION: 'on' });
      await gql('user-management', doc).catch(() => undefined);
      expect(methods[0]).toBe('OPTIONS');
    });
  });
});
