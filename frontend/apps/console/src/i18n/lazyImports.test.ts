// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 A SOURCE-LEVEL GUARD for the lazy English catalogs in i18n/lazy/.
//
// A namespace whose English catalog is registered by i18n/lazy/<ns>.ts is not in config.ts's bundle,
// so a module that translates with it but never imports that file renders raw KEY NAMES whenever it
// is reached before any module that does (another route, a deep link, a test). Nothing fails: the
// build is green and every screen that happens to load its siblings first looks right. So the rule
// "every user of the namespace imports its catalog" is checked here, over the source, the way
// bundle-boundaries.test.ts checks the MapLibre boundary.
//
// Usage is detected the ways the console writes it: useTranslation('ns') / useTranslation(['ns', …]),
// a `ns:key` literal (t('ns:key'), a key held in a constant), and an `ns:` / `ns=` option.

import { describe, expect, it } from 'vitest';

const SOURCES = import.meta.glob('../**/*.{ts,tsx}', { eager: true, query: '?raw', import: 'default' }) as Record<
  string,
  string
>;

const FILES = Object.entries(SOURCES).filter(
  ([p]) => !/\.test\.tsx?$/.test(p) && !p.includes('/gql/') && !p.includes('/i18n/locales/') && !p.includes('/i18n/lazy/'),
);

const LAZY = Object.keys(
  import.meta.glob('./lazy/*.ts', { eager: false }),
).map((p) => p.replace(/^\.\/lazy\//, '').replace(/\.ts$/, ''));

function namespacesUsed(source: string): Set<string> {
  const got = new Set<string>();
  const quoted = /['"`]([A-Za-z]+)['"`]/g;
  for (const m of source.matchAll(/useTranslation\(\s*(\[[^\]]*\]|['"`][^'"`]*['"`])/g)) {
    for (const n of m[1].matchAll(quoted)) got.add(n[1]);
  }
  for (const m of source.matchAll(/['"`]([A-Za-z]+):[A-Za-z_]/g)) got.add(m[1]);
  for (const m of source.matchAll(/\bns[:=]\s*\{?\s*(\[[^\]]*\]|['"`][^'"`]*['"`])/g)) {
    for (const n of m[1].matchAll(quoted)) got.add(n[1]);
  }
  return got;
}

describe('every user of a lazy English catalog imports it', () => {
  it('the scan reaches the lazy catalogs and the files that use them', () => {
    expect(LAZY.length).toBeGreaterThan(4);
    expect(FILES.length).toBeGreaterThan(100);
    expect(FILES.some(([, s]) => namespacesUsed(s).has('deviceProfiles'))).toBe(true);
  });

  for (const ns of LAZY) {
    it(`${ns}`, () => {
      const importsIt = new RegExp(`^\\s*import\\s+['"]@/i18n/lazy/${ns}['"]`, 'm');
      const missing = FILES.filter(([, s]) => namespacesUsed(s).has(ns) && !importsIt.test(s)).map(([p]) => p);
      expect(missing, `these modules translate with "${ns}" but do not import '@/i18n/lazy/${ns}'`).toEqual([]);
    });
  }
});
