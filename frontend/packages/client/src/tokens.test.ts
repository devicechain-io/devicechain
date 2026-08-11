// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  conformsToMask,
  generateToken,
  isValidToken,
  MAX_TOKEN_LEN,
  validateMask,
  sampleToken,
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

describe('validateMask', () => {
  // 🔴 This table is duplicated, case for case, in the tokenmask Go package's
  // TestValidateAcceptsUsableMasks / TestValidateRefusesMasksThatMintSilentlyBrokenTokens.
  // Kept in step BY HAND, deliberately, and not hoisted into a fixture both sides
  // read: a Go test that reads a file outside its own module does not invalidate
  // the test cache when that file changes, so a drifted shared fixture would
  // replay a cached PASS — a link that looks stronger than the duplication while
  // being weaker.
  it('accepts the masks operators actually write', () => {
    for (const mask of [
      '{slug}',
      'device-{alphanumeric-4}',
      'area-{slug}',
      'pin-{numeric-4}',
      '{uuid}',
      'dev_{slug}',
      '{slug}-{numeric-2}',
    ]) {
      expect(validateMask(mask), mask).toBeNull();
    }
  });

  it('names the placeholder that silently generates nothing', () => {
    // The defect: an unknown placeholder contributes NOTHING, so this mints "dev-".
    expect(generateToken('dev-{sulg}', { seed: 'North Yard' })).toBe('dev-');
    expect(validateMask('dev-{sulg}')).toEqual({
      reason: 'unknownPlaceholder',
      placeholder: '{sulg}',
    });
  });

  it('refuses a mask that would give every entity the same token', () => {
    expect(validateMask('device')).toEqual({ reason: 'noPlaceholder' });
  });

  it('refuses a mask whose minted token breaks the platform grammar', () => {
    expect(validateMask('my device-{slug}')?.reason).toBe('invalidToken');
    expect(validateMask('-{slug}')?.reason).toBe('invalidToken');
    expect(validateMask('area/{slug}')?.reason).toBe('invalidToken');
    // Was 'invalidToken' — a width of 500 used to be caught only by sampling it
    // and finding the result too long. It is now caught by the width bound, which
    // is both cheaper and a more accurate description of what is wrong.
    expect(validateMask('{alphanumeric-500}')?.reason).toBe('widthTooLarge');
  });

  it('refuses an empty mask, and a zero-width placeholder that mints one', () => {
    expect(validateMask('')).toEqual({ reason: 'empty' });
    // 🔴 "0" is a TRUTHY string in JS, so the parsed 0 survives `seg.n ?? 8` and
    // this mints "" — where {alphanumeric} mints eight characters.
    expect(generateToken('{alphanumeric-0}')).toBe('');
    expect(validateMask('{alphanumeric-0}')?.reason).toBe('invalidToken');
  });
});

describe('sampleToken', () => {
  it('is deterministic, and is the same generator create forms use', () => {
    expect(sampleToken('area-{slug}')).toBe(sampleToken('area-{slug}'));
    expect(sampleToken('area-{slug}', 'North Yard')).toBe('area-north-yard');
    expect(sampleToken('device-{numeric-3}')).toBe('device-000');
  });
});

describe('absurd widths', () => {
  // 🔴 Found by a differential review against the Go implementation. A width the
  // regexp happily matches can be any size, and `Array.from({length: 1e20})`
  // THROWS RangeError — so validateMask, which the settings editor calls during
  // render, killed the page instead of reporting a problem. A merely huge width
  // did not throw; it froze the tab building a string nobody would ever see
  // (measured at 4.4s for 5e7, per keystroke).
  it('reports an over-wide placeholder instead of throwing', () => {
    expect(validateMask('{alphanumeric-99999999999999999999}')).toEqual({
      reason: 'widthTooLarge',
      placeholder: '{alphanumeric-99999999999999999999}',
      max: MAX_TOKEN_LEN,
    });
    expect(validateMask('{numeric-1000000000}')?.reason).toBe('widthTooLarge');
  });

  it('generates safely from an over-wide mask rather than exploding', () => {
    // Defensive: validateMask now refuses these, but a mask stored BEFORE the
    // server gained its bound is still read by every create form.
    expect(() => generateToken('{alphanumeric-99999999999999999999}')).not.toThrow();
    expect(generateToken('dev-{alphanumeric-99999999999999999999}')).toBe('dev-');
  });

  // The counterweight: the bound must not refuse a width that can mint a legal
  // token, or every mask in the corpus above would fail for the wrong reason.
  it('accepts a width up to the token length bound', () => {
    expect(validateMask(`{numeric-${MAX_TOKEN_LEN}}`)).toBeNull();
    expect(validateMask(`{numeric-${MAX_TOKEN_LEN + 1}}`)?.reason).toBe('widthTooLarge');
  });
});
