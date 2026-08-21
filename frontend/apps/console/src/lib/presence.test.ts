// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 WHY THIS EXISTS. The whole point of `presenceKind` is one distinction — silence
// is not a reported disconnect — and there are exactly two ways to lose it, both of
// which look like a tidy-up in review:
//
//   1. swapping the last two returns, so silence gets the confident wording; and
//   2. rewriting `=== 'ASSERTED'` as `!== 'INFERRED'`, which reads as the same test
//      and inverts the fail-safe for every value that is neither.
//
// Both are pinned below by a table that states the intended answer for each input as
// data, so a rewrite has to change an assertion rather than slip past one.

import { describe, expect, it } from 'vitest';

import { presenceKind, presenceSourceLabelKey, type PresenceKind } from './presence';
import en from '../i18n/locales/en/devices.json';
import es from '../i18n/locales/es/devices.json';

describe('presenceKind', () => {
  // The full cross-product of the two inputs, plus the no-row case.
  const table: Array<[string, Parameters<typeof presenceKind>[0], PresenceKind]> = [
    ['no state row at all', undefined, 'unknown'],
    ['an explicit null row', null, 'unknown'],
    ['asserted and active', { active: true, presenceSource: 'ASSERTED' }, 'online'],
    ['inferred and active', { active: true, presenceSource: 'INFERRED' }, 'online'],
    ['inferred and inactive — silence, not a death', { active: false, presenceSource: 'INFERRED' }, 'quiet'],
    ['asserted and inactive — a transport said so', { active: false, presenceSource: 'ASSERTED' }, 'disconnected'],
  ];

  it.each(table)('classifies %s', (_name, state, expected) => {
    expect(presenceKind(state)).toBe(expected);
  });

  // The fail-safe, stated separately from the table because it is the assertion that
  // dies to `!== 'INFERRED'` while every row above still passes.
  it.each([[''], ['asserted'], ['Asserted'], ['UNKNOWN'], ['DEMOTED'], ['ASSERTED ']])(
    'treats the unrecognised source %j as quiet, never as disconnected',
    (source) => {
      expect(presenceKind({ active: false, presenceSource: source })).toBe('quiet');
    },
  );

  it('still calls an unrecognised source online while the row is active', () => {
    // The fail-safe is about the CONFIDENT wording, not about disbelieving `active`.
    expect(presenceKind({ active: true, presenceSource: 'wat' })).toBe('online');
  });
});

describe('presenceSourceLabelKey', () => {
  it('words the two known sources and refuses to word anything else', () => {
    expect(presenceSourceLabelKey('ASSERTED')).toBe('presenceSourceAsserted');
    expect(presenceSourceLabelKey('INFERRED')).toBe('presenceSourceInferred');
    expect(presenceSourceLabelKey('DEMOTED')).toBeNull();
    expect(presenceSourceLabelKey('')).toBeNull();
  });

  it('names keys that both locales actually carry', () => {
    for (const key of ['presenceSourceAsserted', 'presenceSourceInferred'] as const) {
      expect(en, `en/devices.json is missing ${key}`).toHaveProperty(key);
      expect(es, `es/devices.json is missing ${key}`).toHaveProperty(key);
    }
  });
});

/**
 * 🔴 THE ONLY AUTOMATED GUARD ON THE HIGHEST-RISK STRING EDIT IN THIS CHANGE.
 *
 * The split is worth nothing if the two states render the same words, and Spanish is
 * where that is easy to get wrong: `offline` used to read "Desconectado", which is
 * precisely the word the newly-distinguished `disconnected` state needs. It now reads
 * "Fuera de línea" and "Desconectado" belongs to the asserted case.
 *
 * `i18n/parity.test.ts` compares key SETS and cannot see this — both keys exist in
 * both locales either way. A revert of that one value is a silent collapse of the
 * whole distinction back to one label, in one locale only.
 */
describe('the offline/disconnected label split', () => {
  it.each([
    ['en', en],
    ['es', es],
  ])('gives %s two different words', (_locale, bundle) => {
    const b = bundle as Record<string, string>;
    expect(b.offline).toBeTruthy();
    expect(b.disconnected).toBeTruthy();
    expect(b.offline).not.toBe(b.disconnected);
  });

  it('keeps the English inferred label byte-identical to what shipped', () => {
    // The regression guard for the list view: only the ASSERTED case gains new
    // wording. If this changes, every existing screenshot and every operator's habit
    // changed with it, and that was not the point of the split.
    expect(en.offline).toBe('Offline');
  });
});
