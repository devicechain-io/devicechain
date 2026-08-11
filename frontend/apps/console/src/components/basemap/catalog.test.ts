// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The catalog's pure half. The catalog DATA is gated on the server side — the Go test
// in backend/services/user-management/basemap/catalog_test.go runs the real validator
// over this very file — so what is left to prove here is the two transformations:
// composing a key into a template, and reading a stored URL back as an entry.

import { describe, expect, it } from 'vitest';
import {
  API_KEY_TOKEN,
  PROVIDERS,
  composeTileUrl,
  findProvider,
  needsApiKey,
  recognize,
} from './catalog';

const OSM = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';

function keyedSource() {
  for (const p of PROVIDERS) {
    const s = p.sources.find(needsApiKey);
    if (s) return { provider: p, source: s };
  }
  throw new Error('the catalog has no keyed provider; this suite would be vacuous');
}

describe('composeTileUrl', () => {
  it('substitutes the key', () => {
    const { source } = keyedSource();
    const url = composeTileUrl(source.tileUrl, 'abc123');
    expect(url).not.toContain(API_KEY_TOKEN);
    expect(url).toContain('abc123');
  });

  // 🔴 The fail-closed half. Substituting a blank would compose `?apikey=` — a URL
  // that passes every shape check on both sides and is stored, then 401s on every
  // tile. Leaving the placeholder in makes it a value the client blocks and the
  // server refuses, which is the whole reason the token is not one MapLibre knows.
  it('leaves the placeholder INTACT for a blank key, rather than composing an empty one', () => {
    const { source } = keyedSource();
    expect(composeTileUrl(source.tileUrl, '')).toBe(source.tileUrl);
    expect(composeTileUrl(source.tileUrl, '   ')).toBe(source.tileUrl);
    expect(composeTileUrl(source.tileUrl, '')).toContain(API_KEY_TOKEN);
  });

  it('URL-encodes a key that would otherwise terminate the query string', () => {
    const { source } = keyedSource();
    const url = composeTileUrl(source.tileUrl, 'a&b=c d');
    expect(url).toContain('a%26b%3Dc%20d');
    expect(url).not.toContain('a&b=c d');
  });

  it('leaves a template with no key placeholder alone', () => {
    expect(composeTileUrl(OSM, 'ignored')).toBe(OSM);
  });
});

describe('recognize', () => {
  it('reads a keyless catalog URL back as its entry', () => {
    const m = recognize(OSM);
    expect(m?.provider.id).toBe('openstreetmap');
    expect(m?.source.id).toBe('standard');
    expect(m?.apiKey).toBe('');
  });

  // The round trip is what makes the picker readable rather than write-only: reopening
  // the page on a saved keyed URL has to recover the key, or changing style would
  // silently discard it.
  it('round-trips a key through compose and back', () => {
    const { provider, source } = keyedSource();
    for (const key of ['plainkey123', 'a&b=c d', 'kéy-with_ünicode~', '100%']) {
      const url = composeTileUrl(source.tileUrl, key);
      const m = recognize(url);
      expect(m?.provider.id, `provider for ${key}`).toBe(provider.id);
      expect(m?.source.id, `source for ${key}`).toBe(source.id);
      expect(m?.apiKey, `key for ${key}`).toBe(key);
    }
  });

  it('reports an unfilled key as empty rather than as the placeholder text', () => {
    const { source } = keyedSource();
    expect(recognize(source.tileUrl)?.apiKey).toBe('');
  });

  // 🔴 NEGATIVE CONTROL. Every assertion above is "recognize found something". If it
  // matched too eagerly — say by prefix alone — they would all still pass while the
  // picker mislabelled a private tile server as a public provider and, worse, offered
  // that provider's credit line for it.
  it('returns null for anything the catalog does not describe', () => {
    expect(recognize('')).toBeNull();
    expect(recognize('   ')).toBeNull();
    expect(recognize('https://tiles.internal.example.com/{z}/{x}/{y}.png')).toBeNull();
    // Same host as a catalog entry, different path — a near miss must still miss.
    expect(recognize('https://tile.openstreetmap.org/other/{z}/{x}/{y}.png')).toBeNull();
    // A truncated catalog URL: the prefix matches, the whole value does not.
    expect(recognize('https://tile.openstreetmap.org/')).toBeNull();
  });

  it('does not confuse two entries that share a host', () => {
    const carto = findProvider('carto');
    expect(carto, 'the catalog should still carry a multi-style provider').toBeTruthy();
    for (const s of carto!.sources) {
      expect(recognize(s.tileUrl)?.source.id).toBe(s.id);
    }
  });
});

describe('the catalog data', () => {
  it('carries providers and styles', () => {
    expect(PROVIDERS.length).toBeGreaterThan(0);
    for (const p of PROVIDERS) expect(p.sources.length).toBeGreaterThan(0);
  });

  // Restated from the Go gate on purpose: this one runs in the console's own suite, so
  // an entry added without its credit line fails here too rather than only in a
  // backend job the author may not run.
  it('prefills a non-empty attribution for every style', () => {
    for (const p of PROVIDERS) {
      for (const s of p.sources) {
        expect(s.attribution.trim(), `${p.id}/${s.id}`).not.toBe('');
      }
    }
  });

  // 🔴 The cross-language contract. The Go gate composes with a plain string replace;
  // this side URL-encodes. For a key made only of unreserved characters the two must
  // agree exactly, or the value the backend proved valid is not the value the console
  // actually stores.
  it('composes identically to the server-side gate for an unreserved key', () => {
    const key = 'k3y-with_odd.chars~09';
    expect(encodeURIComponent(key)).toBe(key);
    for (const p of PROVIDERS) {
      for (const s of p.sources) {
        expect(composeTileUrl(s.tileUrl, key)).toBe(s.tileUrl.split(API_KEY_TOKEN).join(key));
      }
    }
  });
});
