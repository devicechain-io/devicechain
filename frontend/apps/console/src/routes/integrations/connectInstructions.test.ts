// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  BEARER_PLACEHOLDER,
  TTS_UPLINK_PATHS,
  authorizationHeader,
  chirpstackFields,
  genericCurl,
  integrationUrl,
  ttsFields,
} from './connectInstructions';

const base = { tenant: 'acme', integrationToken: 'eu868-gw' };

const ORIGIN = window.location.origin;

describe('the connect URL', () => {
  it('is the console origin, the integrations route, the tenant and the integration token', () => {
    expect(integrationUrl(base)).toBe(`${ORIGIN}/api/integrations/acme/eu868-gw`);
    expect(integrationUrl(base)).not.toMatch(/<[a-z-]+>/);
  });

  it('uses a given origin, without doubling its trailing slash', () => {
    expect(integrationUrl({ ...base, origin: 'https://dc.example.com/' })).toBe(
      'https://dc.example.com/api/integrations/acme/eu868-gw',
    );
  });

  it('encodes a tenant or token that needs it, so the URL is one path', () => {
    expect(integrationUrl({ ...base, integrationToken: 'a b/c' })).toContain('/acme/a%20b%2Fc');
  });

  it('never puts the bearer token in the URL', () => {
    expect(integrationUrl({ ...base, bearerToken: 'SECRET-TOKEN' })).not.toContain('SECRET-TOKEN');
  });
});

describe('the header', () => {
  it('holds a placeholder unless the token is being shown for the first time', () => {
    expect(authorizationHeader(base)).toBe(`Authorization: Bearer ${BEARER_PLACEHOLDER}`);
    expect(authorizationHeader({ ...base, bearerToken: 'SECRET-TOKEN' })).toBe('Authorization: Bearer SECRET-TOKEN');
  });
});

describe('per-network-server fields', () => {
  it('ChirpStack: JSON encoding, the endpoint, and the header as a name and a value', () => {
    const f = chirpstackFields({ ...base, bearerToken: 'T' });
    expect(f.map((x) => x.value)).toEqual(['JSON', `${ORIGIN}/api/integrations/acme/eu868-gw`, 'Authorization', 'Bearer T']);
    expect(f.map((x) => x.labelKey).slice(2)).toEqual(['snippetHeaderName', 'snippetHeaderValue']);
    // Only the value holding the real token is masked in captures.
    expect(f.filter((x) => x.secret).map((x) => x.value)).toEqual(['Bearer T']);
    expect(chirpstackFields(base).some((x) => x.secret)).toBe(false);
  });

  it('The Things Stack: the base URL, the header and an uplink path the platform reads', () => {
    const f = ttsFields(base);
    expect(f[0].value).toBe(`${ORIGIN}/api/integrations/acme/eu868-gw`);
    expect(f.slice(1, 3).map((x) => x.value)).toEqual(['Authorization', 'Bearer <bearer-token>']);
    const path = f[3].value.replace(/^\//, '');
    expect(TTS_UPLINK_PATHS).toContain(path);
    expect([...TTS_UPLINK_PATHS]).toEqual(['up', 'uplink', 'uplinks', 'uplink-message']);
  });
});

describe('a generic source', () => {
  it('names a JSON body field where the pointer says', () => {
    const curl = genericCurl(base, { binary: false, idKind: 'pointer', idValue: '/device/id' });
    expect(curl).toContain("-H 'Content-Type: application/json'");
    expect(curl).toContain("-H 'Authorization: Bearer <bearer-token>'");
    expect(curl).toContain(`-d '{"temperature":21.5,"device":{"id":"<device-id>"}}'`);
  });

  it('names the identifier in a header or a query value', () => {
    expect(genericCurl(base, { binary: false, idKind: 'header', idValue: 'X-Id' })).toContain("-H 'X-Id: <device-id>'");
    const q = genericCurl(base, { binary: true, idKind: 'query', idValue: 'dev' });
    expect(q).toContain('/api/integrations/acme/eu868-gw?dev=<device-id>');
    expect(q).toContain('application/octet-stream');
    expect(q).toContain('--data-binary @frame.bin');
  });
});

describe('shell quoting in the generic curl', () => {
  it('quotes a value with a single quote so the shell cannot reinterpret it', () => {
    const curl = genericCurl({ ...base, integrationToken: "it's" }, { binary: false, idKind: 'header', idValue: "X-Id" });
    expect(curl).toContain(String.raw`/acme/it'\''s'`);
  });
});
