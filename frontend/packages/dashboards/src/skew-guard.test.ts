// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The "never refuses its own instance" guard for schema negotiation, for the hand-authored
// dashboard documents (the packages carry no codegen). Each is sent through the real client with
// negotiation on, against the SDL of the service that serves it, and must reach the wire.

import { readFileSync } from 'node:fs';
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
import * as alarms from './internal/alarm-doc';
import * as commands from './internal/command-doc';
import * as locations from './internal/location-doc';
import * as measurements from './internal/measurement-doc';
import * as presence from './internal/presence-doc';

const sdl = (service: string) =>
  readFileSync(new URL(`../../../../backend/services/${service}/graphql/schema.graphql`, import.meta.url), 'utf8');

// Which service serves each module's documents; an entry in `except` is served elsewhere.
const modules: { name: string; exports: Record<string, unknown>; service: string; except?: Record<string, string> }[] = [
  { name: 'queries', exports: queries, service: 'device-management', except: { BUCKETED_MEASUREMENTS: 'event-management' } },
  { name: 'alarm-doc', exports: alarms, service: 'device-management' },
  { name: 'command-doc', exports: commands, service: 'command-delivery' },
  { name: 'location-doc', exports: locations, service: 'device-state' },
  { name: 'measurement-doc', exports: measurements, service: 'event-management' },
  { name: 'presence-doc', exports: presence, service: 'device-state' },
];

const isOperation = (v: unknown): v is string => typeof v === 'string' && /^\s*(query|mutation|subscription)\b/.test(v);

beforeEach(() => {
  setAuthTokenGetter(async () => 'tok');
  enableSchemaNegotiation();
});

afterEach(() => {
  disableSchemaNegotiation();
  setAuthTokenGetter(null);
  vi.unstubAllGlobals();
});

describe('every hand-authored dashboard document is served by its own service', () => {
  for (const mod of modules) {
    for (const [name, doc] of Object.entries(mod.exports).filter(([, v]) => isOperation(v))) {
      const service = mod.except?.[name] ?? mod.service;
      it(`${mod.name}.${name} passes negotiation against ${service}`, async () => {
        const schema = sdl(service);
        const sent: string[] = [];
        vi.stubGlobal(
          'fetch',
          vi.fn(async (_url: string, init: RequestInit) => {
            if (init.method === 'OPTIONS') return new Response(JSON.stringify({ format: 1, schema }), { status: 200 });
            sent.push(init.method as string);
            return new Response(JSON.stringify({ data: {} }), { status: 200 });
          }),
        );
        resetSchemaNegotiation();
        await gql('device-management', doc as unknown as TypedDocument<unknown, Record<string, never>>);
        expect(sent).toEqual(['POST']);
      });
    }
  }

  it('covers the documents (a floor, so an empty enumeration cannot pass)', () => {
    const count = modules.flatMap((m) => Object.values(m.exports).filter(isOperation)).length;
    expect(count).toBeGreaterThanOrEqual(20);
  });
});
