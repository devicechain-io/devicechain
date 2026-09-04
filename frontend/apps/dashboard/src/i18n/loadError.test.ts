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
// What enforces "every code": the SAMPLES mapped type below, which the compiler checks
// against LoadError itself — not a list anyone maintains. Deleting an entry from
// en/load.json reddens the resolution test here; deleting it from es/load.json reddens
// the parity test next door. None of the three is visible to the lint.

import { describe, expect, it } from 'vitest';

import { MAX_PASTE_BYTES, type LoadError } from '../load';
import i18n, { SUPPORTED_LOCALES } from './config';
import { loadErrorKey, loadErrorMessage } from './loadError';

// One or more samples per LoadError variant, KEYED BY THE VARIANT'S OWN CODE so the
// compiler owns the enumeration.
//
// 🔴 THIS WAS A PLAIN ARRAY, AND THE COMMENT ABOVE IT CLAIMED IT WAS "checked against
// the union" WHILE THE CHECK COMPARED IT TO A SECOND HAND-TYPED LIST OF THE SAME FIVE
// STRINGS. Two hand-written lists agreeing with each other is not a derivation from
// the type, and the gap was live: adding a variant to LoadError with a `case` in
// loadErrorKey and its key in NO catalog left `tsc` at rc=0 and the suite at 65/65,
// while the viewer read the raw key. Measured, not reasoned about.
//
// The mapped type closes it. A new variant makes this object literal MISSING A
// PROPERTY, which is an error here at compile time — before any test runs, and without
// anyone remembering that this file exists.
const SAMPLES: { [K in LoadError['code']]: Extract<LoadError, { code: K }>[] } = {
  definitionTooLarge: [{ code: 'definitionTooLarge' }],
  definitionInvalid: [
    { code: 'definitionInvalid', detail: 'Unexpected token' },
    // detail: null is a real input, not a curiosity — loadDashboard produces it
    // whenever the thrown value was not an Error.
    { code: 'definitionInvalid', detail: null },
  ],
  manifestInvalid: [{ code: 'manifestInvalid', detail: 'Unexpected token' }],
  manifestNotObject: [{ code: 'manifestNotObject' }],
  manifestDropped: [
    { code: 'manifestDropped', slots: ['zone'] },
    { code: 'manifestDropped', slots: ['zone', 'sensor'] },
  ],
};

const ALL_SAMPLES: LoadError[] = Object.values(SAMPLES).flat();

describe('loadErrorKey', () => {
  // 🔴 The control the mapped type CANNOT give: `definitionInvalid: []` type-checks
  // perfectly and iterates nothing, so every assertion below would agree about a
  // variant it never touched. Exhaustiveness is now the compiler's job; non-emptiness
  // is still this test's.
  it('has at least one sample for every variant', () => {
    for (const [code, samples] of Object.entries(SAMPLES)) {
      expect(samples.length, `${code} has no samples, so nothing below exercises it`)
        .toBeGreaterThan(0);
    }
  });

  it.each(ALL_SAMPLES)('maps $code to a namespaced load key', (error) => {
    const { key } = loadErrorKey(error);
    expect(key).toMatch(/^load:error[A-Z]/);
  });
});

describe('every load failure has real text in every shipped locale', () => {
  for (const { code } of SUPPORTED_LOCALES) {
    it.each(ALL_SAMPLES)(`${code}: $code renders as prose, not as its key`, async (error) => {
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

  // 🔴 The cap message's whole job is to say what the cap IS, so a hardcoded number in
  // the catalogs made every locale a second, unenforced copy of MAX_PASTE_BYTES — one
  // that would go on confidently quoting the old figure after the constant moved. This
  // asserts the DERIVED value, so it follows the constant instead of pinning today's.
  it.each(SUPPORTED_LOCALES.map((l) => l.code))(
    '%s: states the cap the code actually enforces',
    async (code) => {
      await i18n.changeLanguage(code);
      const text = loadErrorMessage({ code: 'definitionTooLarge' }, (k, p) => i18n.t(k, p));
      expect(text).toContain(`${MAX_PASTE_BYTES / (1 << 20)} MiB`);
    },
  );

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
