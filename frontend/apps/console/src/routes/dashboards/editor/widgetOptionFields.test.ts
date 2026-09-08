// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The catalog half of the derived config panel (ADR-076 half 1).
//
// The wording tables in widgetOptionFields.tsx are exhaustive over the schema by
// construction: `Record<OptionKey, …>` refuses to build until a new option has a label and
// a hint decision, and `Record<EnumValueId, …>` until a new enum value has a label. That
// proves a key was NAMED. It does not prove the name resolves — a table entry pointing at
// `widgetLabelWhatever`, which no catalog defines, compiles perfectly and puts the raw key
// on screen where the field label should be. This closes that half.
//
// 🔴 AND IT DOES NOT ASK t() WHETHER A LOCALE HAS A STRING, because t() cannot answer.
// config.ts sets `fallbackLng: 'en'`, so a key missing from the Spanish catalog resolves to
// the ENGLISH sentence — an ordinary-looking string in the wrong language, which a t()-based
// test passes without a murmur. So everything here reads the catalog through getResource,
// which returns undefined for a key that locale does not define.

import i18n, { SUPPORTED_LOCALES } from '@/i18n/config';
import { describe, expect, it } from 'vitest';

import { OPTION_WORDING } from './widgetOptionFields';

const NS = 'dashboards';

// Every catalog key the panel can ask for, paired with what it words — the label of an
// option, the hint under it, an enum choice, an enum's empty state. Flattened here so a new
// TABLE cannot be added to OPTION_WORDING and go unchecked: the count control at the bottom
// notices a table that contributed nothing.
function wordingKeys(): { where: string; key: string }[] {
  const out: { where: string; key: string }[] = [];
  for (const [option, key] of Object.entries(OPTION_WORDING.labels)) {
    out.push({ where: `label ${option}`, key });
  }
  for (const [option, key] of Object.entries(OPTION_WORDING.hints)) {
    // `null` is the recorded decision that this option needs no hint, not an omission.
    if (key) out.push({ where: `hint ${option}`, key });
  }
  for (const [type, overrides] of Object.entries(OPTION_WORDING.hintOverrides)) {
    for (const [option, key] of Object.entries(overrides ?? {})) {
      if (key) out.push({ where: `hint ${type}.${option}`, key });
    }
  }
  for (const [id, key] of Object.entries(OPTION_WORDING.enumValues)) {
    out.push({ where: `value ${id}`, key });
  }
  for (const [option, key] of Object.entries(OPTION_WORDING.enumPlaceholders)) {
    out.push({ where: `placeholder ${option}`, key });
  }
  return out;
}

const KEYS = wordingKeys();

describe('the config panel wording', () => {
  it('resolves to a real string in every locale the console ships', () => {
    for (const locale of SUPPORTED_LOCALES) {
      for (const { where, key } of KEYS) {
        const text = i18n.getResource(locale.code, NS, key) as string | undefined;
        expect(text, `${locale.code}: ${where} points at ${key}, which that catalog does not define`).toBeTypeOf(
          'string',
        );
        // 🔴 Non-empty, not merely present. An empty string satisfies every "is it there?"
        // check and renders an unlabelled control — the same nothing as a missing field,
        // arrived at through the table rather than around it.
        expect((text ?? '').trim().length, `${locale.code}: ${where} is blank`).toBeGreaterThan(0);
      }
    }
  });

  // The counterweight: the check above is only meaningful while the locales differ. PER KEY,
  // not per catalog — comparing whole bundles would pass while one entry sat untranslated,
  // because the other two hundred still differ.
  it('is actually translated, not copied, in each locale', () => {
    const other = SUPPORTED_LOCALES.filter((l) => l.code !== 'en');
    expect(other.length, 'nothing to compare against — this check compares nothing').toBeGreaterThan(0);
    for (const locale of other) {
      for (const { where, key } of KEYS) {
        expect(
          i18n.getResource(locale.code, NS, key),
          `${locale.code}: ${where} is the English string verbatim`,
        ).not.toBe(i18n.getResource('en', NS, key));
      }
    }
  });

  // The control. Both assertions above iterate a list, and a list that came back empty —
  // a renamed export, a table that stopped being walked — reports perfect agreement about
  // nothing. The floor is the number of distinct option keys the schema declares, which is
  // the smallest this can honestly be.
  it('is checking a non-trivial number of keys', () => {
    expect(KEYS.length).toBeGreaterThan(Object.keys(OPTION_WORDING.labels).length);
    expect(Object.keys(OPTION_WORDING.enumValues).length).toBeGreaterThan(0);
    expect(Object.keys(OPTION_WORDING.enumPlaceholders).length).toBeGreaterThan(0);
  });
});
