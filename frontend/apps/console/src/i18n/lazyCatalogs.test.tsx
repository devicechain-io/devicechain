// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The on-demand locale path, exercised for real. Every other suite runs against the Spanish
// bundles vitest.setup.ts registers up front, so none of them can tell whether the lazy loader
// works at all: a production-only break (a region tag that asks for a catalog that does not
// exist, a failed chunk that never settles i18nReady, a switch that flips before its catalogs
// are in) would leave them all green. Each test resets the module registry and imports
// config.ts fresh, so the init — and the loader — run inside the test.

import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Written out, not imported: importing it from ./config would run config.ts's init before the
// test has seeded storage (see documentLanguage.test.ts for the long form of this).
const LOCALE_KEY = 'dc.locale';

// How many times the es loader was asked — i.e. how many namespace reads went to the chunk.
let esLoads = 0;

/**
 * Stand in for the es chunk at the loader seam (catalogLoaders.ts), never at the locale module:
 * vitest answers concurrent first imports of a mocked module with the REAL one, so a "failing"
 * locale module quietly loads most of Spanish and a failure test tests nothing.
 *
 *  - 'real':      the actual catalogs, through the actual dynamic import.
 *  - 'rejects':   the import fails — a chunk the server no longer has, with nothing claiming it.
 *  - 'undefined': what Vite's preload wrapper resolves when chunkReload HAS claimed the failure
 *                 (preventDefault, then a reload): no rejection, and no module either.
 *  - 'empty':     a chunk that arrived without the namespace asked for.
 */
function esChunk(shape: 'real' | 'rejects' | 'undefined' | 'empty'): void {
  vi.doMock('./catalogLoaders', () => ({
    LOADERS: {
      es: () => {
        esLoads++;
        switch (shape) {
          case 'real':
            return import('./locales/es');
          case 'rejects':
            return Promise.reject(new TypeError('Failed to fetch dynamically imported module'));
          case 'undefined':
            return Promise.resolve(undefined);
          case 'empty':
            return Promise.resolve({ default: {} });
        }
      },
    },
  }));
}

const FAILURES = ['rejects', 'undefined', 'empty'] as const;

const originalLanguages = Object.getOwnPropertyDescriptor(window.navigator, 'languages');
const originalLanguage = Object.getOwnPropertyDescriptor(window.navigator, 'language');

function browserLanguages(...tags: string[]) {
  Object.defineProperty(window.navigator, 'languages', { configurable: true, get: () => tags });
  Object.defineProperty(window.navigator, 'language', { configurable: true, get: () => tags[0] });
}

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  // i18next is an external dependency, so resetModules does NOT give a fresh copy: every test
  // would re-init the one default instance, already initialized and holding the last test's
  // language, which is exactly the state a first page load does not have. Hand config.ts a
  // brand-new instance instead.
  vi.doMock('i18next', async (importOriginal) => {
    const real = await importOriginal<typeof import('i18next')>();
    return { ...real, default: real.default.createInstance() };
  });
  esChunk('real');
  esLoads = 0;
  document.documentElement.lang = 'zz-untouched';
});

afterEach(() => {
  vi.doUnmock('./catalogLoaders');
  vi.doUnmock('i18next');
  if (originalLanguages) Object.defineProperty(window.navigator, 'languages', originalLanguages);
  else delete (window.navigator as unknown as Record<string, unknown>).languages;
  if (originalLanguage) Object.defineProperty(window.navigator, 'language', originalLanguage);
  else delete (window.navigator as unknown as Record<string, unknown>).language;
});

describe('first load', () => {
  it.each(['es-MX', 'es-419', 'es'])('a browser on %s loads the base es catalog', async (tag) => {
    browserLanguages(tag, 'en');
    const { i18nReady, default: i18n } = await import('./config');
    await i18nReady;

    expect(i18n.resolvedLanguage).toBe('es');
    expect(i18n.t('common:back')).toBe('Volver');
    expect(document.documentElement.lang).toBe('es');
  });

  it('an unshipped browser language falls to English and asks for no Spanish', async () => {
    browserLanguages('fr-FR', 'fr');
    const { i18nReady, default: i18n } = await import('./config');
    await i18nReady;

    expect(i18n.resolvedLanguage).toBe('en');
    expect(i18n.t('common:back')).toBe('Back');
    expect(document.documentElement.lang).toBe('en');
    expect(esLoads).toBe(0);
  });

  it('English needs no load: ready and translated on the import itself', async () => {
    localStorage.setItem(LOCALE_KEY, 'en');
    const { default: i18n } = await import('./config');

    // Nothing awaited: this is what lets main.tsx skip the loading screen for English.
    expect(i18n.hasLoadedNamespace('common')).toBe(true);
    expect(i18n.t('common:back')).toBe('Back');
    expect(esLoads).toBe(0);
  });

  it('a Spanish start is NOT ready until the catalog arrives', async () => {
    localStorage.setItem(LOCALE_KEY, 'es');
    const { i18nReady, default: i18n } = await import('./config');

    // main.tsx holds the app back on exactly this answer.
    expect(i18n.hasLoadedNamespace('common')).toBe(false);
    await i18nReady;
    expect(i18n.hasLoadedNamespace('common')).toBe(true);
  });

  it('every namespace arrives in Spanish, not just the default one', async () => {
    localStorage.setItem(LOCALE_KEY, 'es');
    const { i18nReady, default: i18n, NAMESPACES } = await import('./config');
    await i18nReady;

    const missing = NAMESPACES.filter((ns) => !i18n.hasResourceBundle('es', ns));
    expect(missing).toEqual([]);
  });

  it.each(FAILURES)(
    'a chunk that does not deliver (%s) still settles i18nReady, in English',
    async (shape) => {
      esChunk(shape);
      localStorage.setItem(LOCALE_KEY, 'es');
      const { i18nReady, default: i18n } = await import('./config');

      // Must SETTLE — main.tsx renders on .then, so a hang is a console stuck on its loading
      // screen for good.
      await i18nReady;
      expect(esLoads).toBeGreaterThan(0);
      expect(i18n.hasResourceBundle('es', 'common')).toBe(false);
      expect(i18n.resolvedLanguage).toBe('en');
      expect(i18n.t('common:back')).toBe('Back');
      // Announced as what is actually on screen.
      expect(document.documentElement.lang).toBe('en');
    },
  );
});

describe('runtime switch', () => {
  it('en → es waits for the catalog before switching, and es → en needs nothing', async () => {
    localStorage.setItem(LOCALE_KEY, 'en');
    const { i18nReady, default: i18n, setUserLocale } = await import('./config');
    await i18nReady;

    const switched = new Promise<void>((resolve) => {
      const on = (lng: string) => {
        if (lng !== 'es') return;
        i18n.off('languageChanged', on);
        resolve();
      };
      i18n.on('languageChanged', on);
    });
    setUserLocale('es');

    // Still English while the chunk is in flight: no half-Spanish render.
    expect(i18n.resolvedLanguage).toBe('en');
    expect(i18n.t('common:back')).toBe('Back');

    await switched;
    expect(i18n.resolvedLanguage).toBe('es');
    expect(i18n.t('common:back')).toBe('Volver');
    expect(document.documentElement.lang).toBe('es');

    setUserLocale('en');
    // English is bundled: the switch back is synchronous.
    expect(i18n.resolvedLanguage).toBe('en');
    expect(document.documentElement.lang).toBe('en');
  });

  it.each(FAILURES)(
    'a switch whose chunk does not deliver (%s) stays in English and leaves nothing unhandled',
    async (shape) => {
      esChunk(shape);
      localStorage.setItem(LOCALE_KEY, 'en');
      const { i18nReady, default: i18n, setUserLocale } = await import('./config');
      await i18nReady;

      const unhandled = vi.fn();
      process.on('unhandledRejection', unhandled);
      try {
        const settled = new Promise<void>((resolve) => i18n.on('languageChanged', () => resolve()));
        expect(() => setUserLocale('es')).not.toThrow();
        await settled;
        await new Promise((r) => setTimeout(r, 10));
        expect(i18n.resolvedLanguage).toBe('en');
        expect(i18n.t('common:back')).toBe('Back');
        expect(document.documentElement.lang).toBe('en');
        expect(unhandled).not.toHaveBeenCalled();
      } finally {
        process.off('unhandledRejection', unhandled);
      }
    },
  );
});

describe('a component on the lazy path', () => {
  it('renders Spanish copy once i18nReady resolves', async () => {
    localStorage.setItem(LOCALE_KEY, 'es');
    // Fresh copies of react-i18next and the config, so the component reads THIS init.
    const { i18nReady } = await import('./config');
    const { useTranslation } = await import('react-i18next');
    function Probe() {
      const { t } = useTranslation('common');
      return <span>{t('back')}</span>;
    }
    await act(async () => {
      await i18nReady;
    });
    render(<Probe />);
    expect(screen.getByText('Volver')).toBeTruthy();
  });
});
