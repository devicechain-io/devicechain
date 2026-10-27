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

describe('the connect URL', () => {
  it('has the tenant and the integration token, with the unknown host and instance left visible', () => {
    expect(integrationUrl(base)).toBe('https://<ingest-host>/<instance-id>/acme/integrations/eu868-gw');
  });

  it('uses the real host and instance when the caller knows them', () => {
    expect(integrationUrl({ ...base, host: 'ingest.example.com', instanceId: 'prod1' })).toBe(
      'https://ingest.example.com/prod1/acme/integrations/eu868-gw',
    );
  });

  it('encodes a tenant or token that needs it, so the URL is one path', () => {
    expect(integrationUrl({ ...base, integrationToken: 'a b/c' })).toContain('/integrations/a%20b%2Fc');
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
  it('ChirpStack: JSON encoding, the endpoint and the header', () => {
    const f = chirpstackFields({ ...base, bearerToken: 'T' });
    expect(f.map((x) => x.value)).toEqual([
      'JSON',
      'https://<ingest-host>/<instance-id>/acme/integrations/eu868-gw',
      'Authorization: Bearer T',
    ]);
    // Only the line holding the real token is masked in captures.
    expect(f.filter((x) => x.secret).map((x) => x.value)).toEqual(['Authorization: Bearer T']);
    expect(chirpstackFields(base).some((x) => x.secret)).toBe(false);
  });

  it('The Things Stack: the base URL, the header and an uplink path the platform reads', () => {
    const f = ttsFields(base);
    expect(f[0].value).toBe('https://<ingest-host>/<instance-id>/acme/integrations/eu868-gw');
    const path = f[2].value.replace(/^\//, '');
    expect(TTS_UPLINK_PATHS).toContain(path);
    expect([...TTS_UPLINK_PATHS]).toEqual(['up', 'uplink', 'uplinks', 'uplink-message']);
  });
});

describe('a generic source', () => {
  it('names a JSON body field where the pointer says', () => {
    const curl = genericCurl(base, { binary: false, idKind: 'pointer', idValue: '/device/id' });
    expect(curl).toContain("-H 'Content-Type: application/json'");
    expect(curl).toContain(`-d '{"temperature":21.5,"device":{"id":"<device-id>"}}'`);
  });

  it('names the identifier in a header or a query value', () => {
    expect(genericCurl(base, { binary: false, idKind: 'header', idValue: 'X-Id' })).toContain("-H 'X-Id: <device-id>'");
    const q = genericCurl(base, { binary: true, idKind: 'query', idValue: 'dev' });
    expect(q).toContain('integrations/eu868-gw?dev=<device-id>');
    expect(q).toContain('application/octet-stream');
    expect(q).toContain('--data-binary @frame.bin');
  });
});
