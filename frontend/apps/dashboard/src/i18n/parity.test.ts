// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { NAMESPACES, SUPPORTED_LOCALES } from './config';

// The locale JSON as it sits ON DISK, which is what a translator edits and what a
// reviewer reads in a diff. Going through the registry in config.ts instead would test
// the imports rather than the files, and would be blind to a namespace file that exists
// and was never registered — see the last test below, which catches exactly that.
const files = import.meta.glob('./locales/*/*.json', { eager: true }) as Record<
  string,
  { default: Record<string, unknown> }
>;

function bundle(locale: string, ns: string): Record<string, unknown> | undefined {
  return files[`./locales/${locale}/${ns}.json`]?.default;
}

/**
 * 🔴 EVERY LOCALE MUST CARRY EVERY KEY.
 *
 * i18next falls back to the key's own name when a translation is missing, so a key
 * added to `en` and forgotten in `es` renders `errorManifestNotObject` as literal text
 * in the middle of the viewer. Nothing errors and nothing logs; the screen looks broken
 * in a way that reads as a bug in the feature rather than a missing translation.
 *
 * The lint gate that exists (`i18next/no-literal-string`) catches a HARDCODED string in
 * JSX. It cannot see this.
 *
 * 🔴 This mirrors the console's parity test rather than sharing it, for the same reason
 * the i18n config is a copy: the two apps ship independently. What is NOT copied is the
 * control's thresholds at the bottom — those are sized to THIS app's corpus, because a
 * number carried over from a bigger app is a control that cannot fire.
 */
describe('locale parity', () => {
  const locales = SUPPORTED_LOCALES.map((l) => l.code);

  it('has a directory for every supported locale', () => {
    expect(locales.length).toBeGreaterThan(1);
    for (const code of locales) {
      const owned = Object.keys(files).filter((f) => f.startsWith(`./locales/${code}/`));
      expect(owned.length, `locale ${code} has no translation files`).toBeGreaterThan(0);
    }
  });

  it.each(NAMESPACES)('namespace %s has identical keys in every locale', (ns) => {
    const bundles = locales.map((code) => {
      const b = bundle(code, ns);
      expect(b, `${code}/${ns}.json is missing entirely`).toBeDefined();
      return { code, keys: new Set(Object.keys(b as object)) };
    });

    // Compared against the FIRST locale rather than a union, so the failure message
    // names a concrete pair.
    const [base, ...rest] = bundles;
    for (const other of rest) {
      const missing = [...base.keys].filter((k) => !other.keys.has(k));
      const extra = [...other.keys].filter((k) => !base.keys.has(k));

      expect(
        missing,
        `${other.code}/${ns} is missing keys present in ${base.code} — the viewer in ` +
          `${other.code} will render these key NAMES as literal text`,
      ).toEqual([]);
      expect(
        extra,
        `${other.code}/${ns} carries keys ${base.code} does not — either ${base.code} lost a ` +
          `translation or these are dead`,
      ).toEqual([]);
    }
  });

  // The control. Without it the parity assertions above would pass just as well over an
  // empty namespace list or empty bundles — reporting perfect agreement about nothing.
  // The numbers are this app's, not the console's: /dash is three screens, so its whole
  // corpus is smaller than a single console namespace.
  it('is checking a non-trivial number of keys', () => {
    expect(NAMESPACES.length).toBeGreaterThanOrEqual(4);
    const total = NAMESPACES.reduce(
      (n, ns) => n + Object.keys(bundle(locales[0], ns) ?? {}).length,
      0,
    );
    expect(total).toBeGreaterThan(20);
  });

  // The other direction, and the reason this test reads files rather than the registry:
  // a namespace file can exist, be translated in both locales, and be reachable by
  // nothing because it was never added to NAMESPACES. It would then be perfectly
  // parallel and perfectly dead, and every assertion above would be happy.
  it('registers every namespace file that exists', () => {
    const onDisk = new Set(
      Object.keys(files)
        .filter((f) => f.startsWith(`./locales/${locales[0]}/`))
        .map((f) => f.split('/').pop()!.replace(/\.json$/, '')),
    );
    const registered = new Set<string>(NAMESPACES);
    const unregistered = [...onDisk].filter((ns) => !registered.has(ns));
    expect(
      unregistered,
      'these namespace files exist but are not in NAMESPACES, so nothing can load them',
    ).toEqual([]);
  });
});
