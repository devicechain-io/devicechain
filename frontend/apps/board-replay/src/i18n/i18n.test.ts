// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import i18n, { DEFAULT_LOCALE, RESOURCES, SUPPORTED_LOCALES, resolveLocale } from './config';
import { LIVE_PATTERNS, liveMatches } from '../testing/live-scan';

// The catalogs as they sit on disk (what a translator edits), not the registry in config.ts:
// a catalog that exists and was never registered is invisible to a test of the registry.
const files = import.meta.glob('./locales/*/*.json', { eager: true }) as Record<
  string,
  { default: Record<string, string> }
>;

const placeholders = (s: string) => [...s.matchAll(/\{\{\s*(\w+)\s*\}\}/g)].map((m) => m[1]).sort();

describe('chrome catalogs', () => {
  it('has exactly one catalog file per supported locale', () => {
    expect(Object.keys(files).sort()).toEqual(SUPPORTED_LOCALES.map((l) => `./locales/${l}/chrome.json`).sort());
  });

  // Plural forms differ by language (Chinese has only "other"), so keys are compared by
  // their base name, and every plural key must at least carry its "other" form.
  const base = (k: string) => k.replace(/_(zero|one|two|few|many|other)$/, '');
  const baseKeys = (c: Record<string, string>) => [...new Set(Object.keys(c).map(base))].sort();

  it.each(SUPPORTED_LOCALES)('%s carries exactly the keys English does, with the same placeholders', (locale) => {
    const en = files['./locales/en/chrome.json'].default;
    const other = files[`./locales/${locale}/chrome.json`].default;
    expect(baseKeys(other)).toEqual(baseKeys(en));
    for (const [key, value] of Object.entries(other)) {
      const reference = en[key] ?? en[`${base(key)}_other`];
      expect(placeholders(value), `${locale}:${key}`).toEqual(placeholders(reference));
      expect(value.trim(), `${locale}:${key} is empty`).not.toBe('');
    }
    for (const key of baseKeys(other).filter((k) => Object.keys(other).includes(`${k}_other`) || Object.keys(en).includes(`${k}_other`))) {
      expect(other[`${key}_other`], `${locale}:${key}_other`).toBeTruthy();
    }
  });

  it('registers the same bundles that are on disk', () => {
    for (const locale of SUPPORTED_LOCALES) {
      expect(RESOURCES[locale].chrome).toEqual(files[`./locales/${locale}/chrome.json`].default);
    }
  });

  it('never uses the word "live", in any locale', () => {
    for (const locale of SUPPORTED_LOCALES) {
      for (const [key, value] of Object.entries(files[`./locales/${locale}/chrome.json`].default)) {
        expect(liveMatches(value), `${locale}:${key} = ${value}`).toEqual([]);
      }
    }
  });

  it('puts the fixed phrase first in the label, with the date and the run after it', () => {
    expect(files['./locales/en/chrome.json'].default.replayLabel).toBe(
      'Replay of a recorded run · {{date}} · {{runId}}',
    );
  });
});

describe('the "live" scanner', () => {
  // Negative control: a scanner that matches nothing would make the test above vacuous.
  it('sees the word in English, in Spanish and in Chinese', () => {
    expect(liveMatches('Selection is available on the live dashboard.')).not.toEqual([]);
    expect(liveMatches('Datos en vivo')).not.toEqual([]);
    expect(liveMatches('实时数据')).not.toEqual([]);
    expect(liveMatches('Datos en directo')).not.toEqual([]);
    expect(liveMatches('Datos en tiempo real')).not.toEqual([]);
    expect(LIVE_PATTERNS.length).toBeGreaterThan(2);
  });

  it('does not flag words that merely contain it', () => {
    expect(liveMatches('delivered, olives, Olivera')).toEqual([]);
  });
});

describe('plural forms', () => {
  it('count the machines in the singular where the language has one', async () => {
    await i18n.changeLanguage('en');
    expect(i18n.t('provenanceMachines', { count: 1 })).toBe('1 simulated machine, its own DeviceChain device.');
    expect(i18n.t('provenanceMachines', { count: 19 })).toBe('19 simulated machines, each its own DeviceChain device.');
    await i18n.changeLanguage('es');
    expect(i18n.t('provenanceMachines', { count: 1 })).toContain('1 máquina simulada');
    await i18n.changeLanguage('zh-CN');
    expect(i18n.t('provenanceMachines', { count: 1 })).toContain('1 台模拟机器');
    await i18n.changeLanguage('en');
  });
});

describe('resolveLocale', () => {
  it.each([
    ['es', 'es'],
    ['es-MX', 'es'],
    ['zh', 'zh-CN'],
    ['zh-CN', 'zh-CN'],
    ['zh-Hans-CN', 'zh-CN'],
    ['fr', 'en'],
    ['', DEFAULT_LOCALE],
    [null, DEFAULT_LOCALE],
  ])('%s -> %s', (tag, want) => {
    expect(resolveLocale(tag)).toBe(want);
  });
});
