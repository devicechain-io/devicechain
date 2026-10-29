// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The catalog half of the derived config panel (ADR-076 half 1).
//
// A built-in's descriptor names the console message ids that word its options, choices and
// titles. The descriptor is data, so nothing compiles those names against a catalog: an id
// pointing at `widgetLabelWhatever`, which no catalog defines, would fall back to the
// portable text (or, for a built-in that ships none, to the humanized key) instead of the
// translated sentence. This proves every id a built-in declares resolves, in every locale.
//
// 🔴 AND IT DOES NOT ASK t() WHETHER A LOCALE HAS A STRING, because t() cannot answer.
// config.ts sets `fallbackLng: 'en'`, so a key missing from the Spanish catalog resolves to
// the ENGLISH sentence — an ordinary-looking string in the wrong language, which a t()-based
// test passes without a murmur. So everything here reads the catalog through getResource,
// which returns undefined for a key that locale does not define.

import i18n, { SUPPORTED_LOCALES } from '@/i18n/config';
import { builtinWidgetDescriptors } from '@devicechain/widgets';
import { describe, expect, it } from 'vitest';
import '@/i18n/lazy/dashboards';

const NS = 'dashboards';

// Every message id the built-in descriptors declare, paired with what it words.
function wordingKeys(): { where: string; key: string }[] {
  const out: { where: string; key: string }[] = [];
  const push = (where: string, key: string | undefined) => {
    if (key) out.push({ where, key });
  };
  for (const [type, d] of Object.entries(builtinWidgetDescriptors)) {
    push(`${type} title`, d.display.titleId);
    push(`${type} category`, d.display.categoryId);
    for (const [option, spec] of Object.entries(d.options) as [string, import('@devicechain/dashboards').OptionSpec][]) {
      push(`${type}.${option} label`, spec.labelId);
      push(`${type}.${option} help`, spec.helpId);
      if (spec.kind === 'enum') {
        push(`${type}.${option} placeholder`, spec.placeholderId);
        for (const [value, key] of Object.entries(spec.valueLabelIds ?? {})) {
          push(`${type}.${option}:${value}`, key);
        }
      }
    }
  }
  return out;
}

const KEYS = wordingKeys();

const SAME_IN_EVERY_LOCALE: ReadonlySet<string> = new Set(['widgetMenuDetail', 'widgetCategoryControl']);

describe('the config panel wording', () => {
  it('resolves to a real string in every locale the console ships', () => {
    for (const locale of SUPPORTED_LOCALES) {
      for (const { where, key } of KEYS) {
        const text = i18n.getResource(locale.code, NS, key) as string | undefined;
        expect(text, `${locale.code}: ${where} points at ${key}, which that catalog does not define`).toBeTypeOf(
          'string',
        );
        // 🔴 Non-empty, not merely present. An empty string satisfies every "is it there?"
        // check and renders an unlabelled control — the same nothing as a missing field.
        expect((text ?? '').trim().length, `${locale.code}: ${where} is blank`).toBeGreaterThan(0);
      }
    }
  });

  // The counterweight: the check above is only meaningful while the locales differ. PER KEY,
  // not per catalog — comparing whole bundles would pass while one entry sat untranslated.
  it('is actually translated, not copied, in each locale', () => {
    const other = SUPPORTED_LOCALES.filter((l) => l.code !== 'en');
    expect(other.length, 'nothing to compare against — this check compares nothing').toBeGreaterThan(0);
    for (const locale of other) {
      for (const { where, key } of KEYS) {
        const en = i18n.getResource('en', NS, key);
        const here = i18n.getResource(locale.code, NS, key);
        // The few strings that are legitimately identical in the two languages: the version
        // detail line is a template of ids and numbers, and "Control" is the same word.
        if (SAME_IN_EVERY_LOCALE.has(key)) continue;
        expect(here, `${locale.code}: ${where} is the English string verbatim`).not.toBe(en);
      }
    }
  });

  // A built-in is translated through ids, so each one must DECLARE its wording: a built-in
  // option with no labelId would show a humanized key in Spanish and English alike, which
  // is understandable and wrong.
  it('is declared for every built-in option, enum choice and enum placeholder', () => {
    for (const [type, d] of Object.entries(builtinWidgetDescriptors)) {
      expect(d.display.titleId, `${type} title`).toBeTruthy();
      expect(d.display.categoryId, `${type} category`).toBeTruthy();
      for (const [option, spec] of Object.entries(d.options) as [string, import('@devicechain/dashboards').OptionSpec][]) {
        expect(spec.labelId, `${type}.${option} has no labelId`).toBeTruthy();
        if (spec.kind === 'enum') {
          expect(spec.placeholderId, `${type}.${option} has no placeholderId`).toBeTruthy();
          for (const value of spec.values) {
            expect(spec.valueLabelIds?.[value], `${type}.${option}:${value} has no label`).toBeTruthy();
          }
        }
      }
    }
  });

  // The control. Every assertion above iterates a list, and a list that came back empty — a
  // renamed export, a descriptor that stopped being walked — reports perfect agreement
  // about nothing.
  it('is checking a non-trivial number of keys', () => {
    expect(KEYS.length).toBeGreaterThan(60);
  });
});
