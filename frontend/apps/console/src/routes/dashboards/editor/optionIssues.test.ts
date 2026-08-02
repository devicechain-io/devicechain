// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The catalog half of the option-issue wording gate.
//
// The Record<OptionIssueCode, string> in optionIssues.ts makes tsc refuse a new issue
// code until it is mapped to a translation key. That is only half the contract: a key
// mapped to a string no catalog defines still builds, and the UI then shows something
// wrong in a way nothing else notices. This closes the other half.
//
// 🔴 AND IT DOES NOT ASK t() WHETHER A LOCALE HAS A STRING, because t() cannot answer.
// config.ts sets `fallbackLng: 'en'`, so a key missing from the Spanish catalog resolves
// to the ENGLISH sentence — a perfectly ordinary-looking string, in the wrong language,
// which a test built on t() passes without a murmur. (Verified: deleting an `es` entry
// left the first draft of this file green.) So the per-locale check reads the catalog
// through getResource, which returns undefined for a key that locale does not define,
// and only the "is it a key echo" check below goes through t().

import i18n, { SUPPORTED_LOCALES } from '@/i18n/config';
import { describe, expect, it } from 'vitest';

import { OPTION_ISSUE_MESSAGE_KEYS } from './optionIssues';

const NS = 'dashboards';
const CODES = Object.entries(OPTION_ISSUE_MESSAGE_KEYS);

describe('option issue wording', () => {
  it('gives every issue code its own string in every locale the console ships', () => {
    for (const locale of SUPPORTED_LOCALES) {
      for (const [code, key] of CODES) {
        const where = `${locale.code}/${code}`;
        const text = i18n.getResource(locale.code, NS, key) as string | undefined;
        expect(text, `${where} is missing from the ${locale.code} catalog`).toBeTypeOf('string');
        expect((text ?? '').length, where).toBeGreaterThan(0);
        // Every one of these names the option it is about. An entry that dropped the
        // interpolation would read as a general complaint on a board where only one of
        // twelve widgets is wrong.
        expect(text, `${where} does not interpolate the option name`).toContain('{{key}}');
      }
    }
  });

  // The mapping can also point at a key NO locale defines — the `Record` is exhaustive
  // over the codes, not over the catalog — and i18next renders a missing key as the key
  // itself. That is the one failure t() is the right instrument for.
  it('maps every code to a key that resolves rather than echoing', async () => {
    await i18n.changeLanguage('en');
    for (const [code, key] of CODES) {
      expect(i18n.t(`${NS}:${key}`, { key: 'someOption' }), code).not.toBe(key);
    }
  });

  // The counterweight: the checks above are only meaningful while the locales differ.
  // 🔴 PER KEY, not per catalog — comparing the whole rendered list would pass while
  // one entry was an untranslated copy of the English, since the other six still
  // differ. (Verified: that is exactly what the first draft of this file did.)
  it('is actually translated, not copied, in each locale', () => {
    const other = SUPPORTED_LOCALES.filter((l) => l.code !== 'en');
    expect(other.length, 'nothing to compare against — this check compares nothing').toBeGreaterThan(0);
    for (const locale of other) {
      for (const [code, key] of CODES) {
        expect(
          i18n.getResource(locale.code, NS, key),
          `${locale.code}/${code} is the English string verbatim`,
        ).not.toBe(i18n.getResource('en', NS, key));
      }
    }
  });
});
