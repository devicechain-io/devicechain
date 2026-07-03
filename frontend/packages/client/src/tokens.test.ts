// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  conformsToMask,
  generateToken,
  isValidToken,
  normalizeToken,
  parseMask,
  resolveMask,
} from './tokens';

// A deterministic RNG so generated ids are stable in tests.
function seqRandom(values: number[]): () => number {
  let i = 0;
  return () => values[i++ % values.length];
}

describe('normalizeToken', () => {
  it('kebab-cases human input', () => {
    expect(normalizeToken('Ops Overview')).toBe('ops-overview');
    expect(normalizeToken('  Fleet_Health  ')).toBe('fleet-health');
    expect(normalizeToken('Zone 3 — East!')).toBe('zone-3-east');
    expect(normalizeToken('a---b__c')).toBe('a-b-c');
    expect(normalizeToken('--edge--')).toBe('edge');
  });

  it('is idempotent', () => {
    const once = normalizeToken('Ops Overview');
    expect(normalizeToken(once)).toBe(once);
  });
});

describe('parseMask', () => {
  it('splits literals and placeholders', () => {
    expect(parseMask('device-{alphanumeric-8}')).toEqual([
      { kind: 'literal', text: 'device-' },
      { kind: 'placeholder', type: 'alphanumeric', n: 8, raw: '{alphanumeric-8}' },
    ]);
  });

  it('marks an unknown placeholder', () => {
    const segs = parseMask('{frobnicate-3}');
    expect(segs).toEqual([{ kind: 'placeholder', type: 'unknown', n: 3, raw: '{frobnicate-3}' }]);
  });
});

describe('generateToken', () => {
  it('fills placeholders and keeps literals', () => {
    // random always 0 → first readable char 'a'; uuid injected.
    const tok = generateToken('device-{alphanumeric-4}', { random: () => 0 });
    expect(tok).toBe('device-aaaa');
  });

  it('draws readable chars from a spread of the alphabet', () => {
    const tok = generateToken('{alphanumeric-3}', { random: seqRandom([0, 0.5, 0.999]) });
    expect(tok).toMatch(/^[a-z0-9]{3}$/);
    // No ambiguous characters ever appear.
    expect(tok).not.toMatch(/[01oil]/);
  });

  it('fills {numeric-N} with digits', () => {
    expect(generateToken('pin-{numeric-4}', { random: () => 0 })).toBe('pin-0000');
  });

  it('derives {slug} from the seed', () => {
    expect(generateToken('{slug}', { seed: 'North Yard' })).toBe('north-yard');
    expect(generateToken('area-{slug}', { seed: 'Bay 12' })).toBe('area-bay-12');
  });

  it('falls back to a readable id for {slug} with no seed', () => {
    const tok = generateToken('{slug}', { random: () => 0 });
    expect(tok).toBe('aaaaaa');
  });

  it('uses the injected uuid', () => {
    expect(generateToken('{uuid}', { uuid: () => 'fixed-uuid' })).toBe('fixed-uuid');
  });

  it('always produces a token that passes the security grammar', () => {
    for (const mask of ['device-{alphanumeric-8}', '{slug}', 'sensor-{slug}-{numeric-4}']) {
      const tok = generateToken(mask, { seed: 'Sample Name', random: () => 0.3 });
      expect(isValidToken(tok)).toBe(true);
    }
  });
});

describe('conformsToMask', () => {
  it('accepts a conforming token and rejects a non-conforming one', () => {
    expect(conformsToMask('device-{alphanumeric-8}', 'device-abc45xyz')).toBe(true);
    expect(conformsToMask('device-{alphanumeric-8}', 'device-abc')).toBe(false); // too short
    expect(conformsToMask('device-{alphanumeric-8}', 'widget-abc45xyz')).toBe(false); // wrong prefix
  });

  it('matches a slug mask', () => {
    expect(conformsToMask('{slug}', 'ops-overview')).toBe(true);
    expect(conformsToMask('{slug}', 'Ops Overview')).toBe(false);
  });

  it('matches a uuid mask', () => {
    expect(conformsToMask('{uuid}', '550e8400-e29b-41d4-a716-446655440000')).toBe(true);
    expect(conformsToMask('{uuid}', 'not-a-uuid')).toBe(false);
  });
});

describe('isValidToken', () => {
  it('mirrors the backend security grammar', () => {
    for (const ok of ['device-1', 'SDK7GV3WXZ3FBXZ', 'plant_07', 'a']) {
      expect(isValidToken(ok)).toBe(true);
    }
    for (const bad of ['', 'bad.token', 'a*b', 'a>b', 'a/b', '-lead', 'has space']) {
      expect(isValidToken(bad)).toBe(false);
    }
    expect(isValidToken('a'.repeat(129))).toBe(false);
  });
});

describe('resolveMask', () => {
  it('prefers the type, then default, then a bare slug', () => {
    const masks = { device: 'device-{alphanumeric-8}', default: '{slug}' };
    expect(resolveMask(masks, 'device')).toBe('device-{alphanumeric-8}');
    expect(resolveMask(masks, 'area')).toBe('{slug}');
    expect(resolveMask({}, 'anything')).toBe('{slug}');
  });
});
