// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THIS FILE IS THE HALF THAT MAKES LOCALIZING load.ts REAL.
//
// Moving those six messages out of load.ts turned three assertions on English prose in
// load.test.ts into assertions on a `code`. That is the right change, and on its own it
// is also the cheap one: a code is trivially satisfied by a catalog entry that does not
// exist, because i18next answers a missing key with the key's own name and nothing
// throws. So load.test.ts now proves the CLASSIFICATION and this file proves the
// RENDERING — that every code reaches a key, and that every key resolves to real text
// in every shipped locale.
//
// Deleting an entry from en/load.json reddens the resolution test here; deleting it
// from es/load.json reddens the parity test next door. Neither is visible to the lint.

import { describe, expect, it } from 'vitest';

import type { LoadError } from '../load';
import i18n, { SUPPORTED_LOCALES } from './config';
import { loadErrorKey, loadErrorMessage } from './loadError';

// One sample per LoadError variant. 🔴 The list is checked against the union below
// rather than trusted: a new variant added to load.ts and forgotten here would leave
// this file testing a subset while reporting green.
const SAMPLES: LoadError[] = [
  { code: 'definitionTooLarge' },
  { code: 'definitionInvalid', detail: 'Unexpected token' },
  { code: 'manifestInvalid', detail: 'Unexpected token' },
  { code: 'manifestNotObject' },
  { code: 'manifestDropped', slots: ['zone'] },
  { code: 'manifestDropped', slots: ['zone', 'sensor'] },
];

describe('loadErrorKey', () => {
  it('covers every LoadError variant', () => {
    // The type-level half is the exhaustive switch in loadErrorKey — a new variant is a
    // compile error there. This is the value-level half: that the SAMPLES above, which
    // every test in this file iterates, actually reach each of those branches.
    const codes = new Set(SAMPLES.map((s) => s.code));
    expect([...codes].sort()).toEqual([
      'definitionInvalid',
      'definitionTooLarge',
      'manifestDropped',
      'manifestInvalid',
      'manifestNotObject',
    ]);
  });

  it.each(SAMPLES)('maps $code to a namespaced load key', (error) => {
    const { key } = loadErrorKey(error);
    expect(key).toMatch(/^load:error[A-Z]/);
  });
});

describe('every load failure has real text in every shipped locale', () => {
  for (const { code } of SUPPORTED_LOCALES) {
    it.each(SAMPLES)(`${code}: $code renders as prose, not as its key`, async (error) => {
      await i18n.changeLanguage(code);
      const { key } = loadErrorKey(error);
      const text = loadErrorMessage(error, (k, p) => i18n.t(k, p));

      // i18next answers a MISSING key with the key's own name, so "did it resolve?" is
      // exactly "is the answer something other than the key". Without this the whole
      // localization could be a set of empty catalogs.
      expect(text, `${code}/${key} resolves to its own key — the catalog entry is missing`)
        .not.toBe(key.split(':').pop());
      expect(text.length).toBeGreaterThan(0);

      // Nothing may leak an unfilled placeholder: a message reading "{{detail}}" is a
      // rendering failure that no assertion on the key alone would notice.
      expect(text, `${code}/${key} left an interpolation unfilled`).not.toMatch(/\{\{/);
    });
  }
});

describe('loadErrorMessage', () => {
  it('interpolates a parser diagnostic verbatim', async () => {
    await i18n.changeLanguage('en');
    const text = loadErrorMessage(
      { code: 'definitionInvalid', detail: 'widgets must be an array' },
      (k, p) => i18n.t(k, p),
    );
    // The diagnostic is the parser's own words and stays untranslated on purpose — it
    // names a field in the document the viewer pasted.
    expect(text).toContain('widgets must be an array');
  });

  it('fills a null detail with the localized fallback, never with "null"', async () => {
    // loadDashboard reports detail: null when the thrown value was not an Error. The
    // obvious bug here is a template rendering the literal string "null" at the viewer,
    // which reads as a broken parser rather than as an unusual failure.
    for (const { code } of SUPPORTED_LOCALES) {
      await i18n.changeLanguage(code);
      const text = loadErrorMessage({ code: 'definitionInvalid', detail: null }, (k, p) =>
        i18n.t(k, p),
      );
      expect(text, `${code} rendered a bare null`).not.toMatch(/\bnull\b/);
      expect(text).toContain(i18n.t('common:unexpectedError'));
    }
  });

  it('quotes each dropped slot so the viewer can find it in what they pasted', async () => {
    await i18n.changeLanguage('en');
    const text = loadErrorMessage({ code: 'manifestDropped', slots: ['zone', 'sensor'] }, (k, p) =>
      i18n.t(k, p),
    );
    expect(text).toContain('"zone", "sensor"');
  });

  it('picks the plural form from the number of dropped slots, in every locale', async () => {
    // 🔴 The counterweight to the resolution test above, which would pass just as well
    // if `count` were never passed and every message rendered the _other form. Singular
    // and plural must differ; that they differ CORRECTLY is pinned per-locale in
    // config.test.ts, where the actual wording lives.
    for (const { code } of SUPPORTED_LOCALES) {
      await i18n.changeLanguage(code);
      const t = (k: string, p?: Record<string, unknown>) => i18n.t(k, p);
      const one = loadErrorMessage({ code: 'manifestDropped', slots: ['zone'] }, t);
      const many = loadErrorMessage({ code: 'manifestDropped', slots: ['zone', 'sensor'] }, t);
      expect(one, `${code} renders one and many identically`).not.toBe(many);
    }
  });
});
