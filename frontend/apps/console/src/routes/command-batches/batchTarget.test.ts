// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  buildDeviceTargets,
  isVersionedGroup,
  parsePastedTokens,
  resolvedClaim,
  MAX_BATCH_DEVICES,
} from './batchTarget';

describe('parsePastedTokens', () => {
  // A paste comes from wherever the operator had the list. A box that understood only
  // newlines reads a spreadsheet row as ONE enormous token, and the batch then refuses a
  // "device" nobody named.
  it('accepts newline-, comma- and space-separated lists alike', () => {
    expect(parsePastedTokens('a\nb\nc')).toEqual(['a', 'b', 'c']);
    expect(parsePastedTokens('a,b,c')).toEqual(['a', 'b', 'c']);
    expect(parsePastedTokens('a b c')).toEqual(['a', 'b', 'c']);
    expect(parsePastedTokens('a, b\nc,\n\nd ')).toEqual(['a', 'b', 'c', 'd']);
  });

  it('yields nothing for an empty or whitespace-only paste', () => {
    expect(parsePastedTokens('')).toEqual([]);
    expect(parsePastedTokens('  \n , \n ')).toEqual([]);
  });
});

describe('buildDeviceTargets', () => {
  // 🔴🔴 THE ONE THAT MATTERS. Order is part of the request: a partially-admitted batch
  // admits in the order the devices were named, so this is how an operator states which
  // devices matter most. Sorting or set-ifying here would silently discard that.
  it('keeps the order devices were named, picker first then paste', () => {
    const { tokens } = buildDeviceTargets(['z', 'a'], 'm\nb');
    expect(tokens).toEqual(['z', 'a', 'm', 'b']);
  });

  // A device named twice is ONE device. Sending twice would be two physical actuations
  // from one line of a list the operator believes names it once — and the collapse is
  // reported rather than silent, since a paste that halves in size is worth knowing about.
  it('collapses a repeat to its first position and counts it', () => {
    const { tokens, duplicates } = buildDeviceTargets(['a', 'b'], 'a\nc\nb\nb');
    expect(tokens).toEqual(['a', 'b', 'c']);
    expect(duplicates).toBe(3);
  });

  it('trims each token and drops blank entries', () => {
    expect(buildDeviceTargets([' a '], ' b ,\n\n c ').tokens).toEqual(['a', 'b', 'c']);
  });

  // The cap is the service's, mirrored for inline feedback. Exactly at the cap is fine;
  // one past it is not — an off-by-one here refuses a legal batch or waves through an
  // illegal one, and both are discovered at the end of a long paste.
  it('flags the set as over the cap only past the cap', () => {
    const atCap = Array.from({ length: MAX_BATCH_DEVICES }, (_, i) => `d-${i}`);
    expect(buildDeviceTargets(atCap, '').overCap).toBe(false);
    expect(buildDeviceTargets(atCap, 'one-more').overCap).toBe(true);
  });

  it('is empty, unduplicated and under cap with nothing named', () => {
    expect(buildDeviceTargets([], '')).toEqual({ tokens: [], duplicates: 0, overCap: false });
  });
});

describe('isVersionedGroup', () => {
  // 🔴 The wire value is LOWERCASE. Getting this backwards is not cosmetic: naming a
  // version for a STATIC group is refused by the service rather than ignored, and
  // withholding the input from a DYNAMIC group pins a fleet actuation to whatever
  // selector was published most recently instead of the one the operator meant.
  it('is true only for a dynamic group, whatever the casing', () => {
    expect(isVersionedGroup('dynamic')).toBe(true);
    expect(isVersionedGroup('DYNAMIC')).toBe(true);
    expect(isVersionedGroup(' Dynamic ')).toBe(true);
    expect(isVersionedGroup('static')).toBe(false);
    expect(isVersionedGroup('STATIC')).toBe(false);
  });

  // An unknown or absent mode is treated as unversioned, which is the safe direction:
  // the version input is withheld rather than offered on a group that would refuse it.
  it('is false for an unknown or missing mode', () => {
    expect(isVersionedGroup('')).toBe(false);
    expect(isVersionedGroup(null)).toBe(false);
    expect(isVersionedGroup(undefined)).toBe(false);
    expect(isVersionedGroup('something-else')).toBe(false);
  });
});

describe('resolvedClaim', () => {
  // 🔴🔴 NULL IS NOT ZERO, and this is the assertion that stops `?? 0` from coming back.
  // Null means no target set was ever established — the refusal came first. Zero means a
  // target that genuinely matched nothing. Told the second when the first is true, an
  // operator goes off to debug a group that may be perfectly healthy.
  it('separates an unestablished target from one that matched nothing', () => {
    expect(resolvedClaim(null)).toEqual({ kind: 'unknown' });
    expect(resolvedClaim(undefined)).toEqual({ kind: 'unknown' });
    expect(resolvedClaim(0)).toEqual({ kind: 'none' });
    expect(resolvedClaim(0)).not.toEqual(resolvedClaim(null));
  });

  it('carries the count through when there was one', () => {
    expect(resolvedClaim(4312)).toEqual({ kind: 'some', count: 4312 });
  });
});
