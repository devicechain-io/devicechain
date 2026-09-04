// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts): config.ts initializes the
// browser language detector and the seam functions touch localStorage/window.

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import i18n, {
  SUPPORTED_LOCALES,
  DEFAULT_LOCALE,
  LOCALE_STORAGE_KEY,
  setUserLocale,
  applyTenantDefaultLocale,
} from './config';

beforeEach(async () => {
  localStorage.clear();
  await i18n.changeLanguage(DEFAULT_LOCALE);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('i18n config', () => {
  it('resolves the English catalog across namespaces', () => {
    expect(i18n.t('login:signIn')).toBe('Sign in');
    // Cross-namespace lookup: the default namespace is `common`.
    expect(i18n.t('common:back')).toBe('Back');
    expect(i18n.t('back')).toBe('Back');
  });

  it('falls back to the key for a missing string rather than crashing', () => {
    expect(i18n.t('login:doesNotExist')).toBe('doesNotExist');
  });

  it('only advertises locales it actually ships', () => {
    // supportedLngs is derived from the registry, so the detector can never
    // resolve to a locale whose catalog is absent. i18next appends 'cimode'.
    const supported = (i18n.options.supportedLngs || []).filter((l) => l !== 'cimode');
    expect(supported).toEqual(SUPPORTED_LOCALES.map((l) => l.code));
    expect(SUPPORTED_LOCALES.map((l) => l.code)).toContain(DEFAULT_LOCALE);
  });

  it('every shipped locale carries a switcher label and code badge', () => {
    // The switcher renders the `badge` chip + `label` per locale; a blank either
    // would show an empty pill. Endonym labels are never translated, so pin they
    // exist here.
    for (const l of SUPPORTED_LOCALES) {
      expect(l.label.length).toBeGreaterThan(0);
      expect(l.badge.length).toBeGreaterThan(0);
    }
  });

  it('never writes the user-choice key on a plain language change (caches: [])', async () => {
    // Directly pins detection.caches === []: only the switcher (setUserLocale)
    // may write LOCALE_STORAGE_KEY, so its presence stays an unambiguous "user
    // chose" signal for the tenant-default seam. If caches reverted to the
    // detector default ['localStorage'], changeLanguage would cache the language
    // here and this reddens.
    await i18n.changeLanguage('en');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });
});

describe('setUserLocale', () => {
  it('persists a shipped locale as the explicit user choice and switches to it', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    setUserLocale('en');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('en');
    expect(spy).toHaveBeenCalledWith('en');
  });

  it('ignores a locale the console does not ship', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    setUserLocale('fr');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
    expect(spy).not.toHaveBeenCalled();
  });
});

// The precedence contract sub-workstream (d) inherits: a tenant default only
// fills in for a user who has not chosen, and only with a shipped locale.
describe('applyTenantDefaultLocale (ADR-066 rung-2 seam)', () => {
  it('is a no-op for an empty/absent tenant default', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale(null);
    applyTenantDefaultLocale(undefined);
    applyTenantDefaultLocale('');
    expect(spy).not.toHaveBeenCalled();
  });

  it('does not select a locale the console does not ship', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('fr');
    expect(spy).not.toHaveBeenCalled();
  });

  it('loses to an explicit user choice already in localStorage', () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'en');
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('en');
    expect(spy).not.toHaveBeenCalled();
  });

  it('is NOT blocked by a stored choice that no longer resolves to a shipped locale', () => {
    // A stale/unshipped stored value has no runtime effect, so an effective
    // tenant default must still apply — an ineffective rung-1 must not suppress
    // an effective rung-2. Deleting the stored-value validation in the guard
    // reddens this.
    localStorage.setItem(LOCALE_STORAGE_KEY, 'xx-not-shipped');
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('en');
    expect(spy).toHaveBeenCalledWith('en');
  });

  it('applies a shipped tenant default when the user has made no choice', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('en');
    expect(spy).toHaveBeenCalledWith('en');
  });

  // 🔴 RUNG 2 MUST BEAT RUNG 3. This is the assertion the seam exists for, and the one
  // the tests above cannot make: everything else here checks rung 2 against rung 1, and
  // a `applyTenantDefaultLocale` that bailed out whenever a language was already
  // resolved would pass all of them.
  //
  // The browser rung produces a resolved language before this ever runs (detection
  // happens at init, and `caches: []` means nothing is stored), so a language in effect
  // with an empty LOCALE_STORAGE_KEY is exactly the state rung 3 leaves behind. The
  // tenant default has to overwrite it.
  it('beats a language the browser rung already resolved', async () => {
    await i18n.changeLanguage('es');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull(); // nothing was CHOSEN

    applyTenantDefaultLocale('en');

    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('en'));
  });
});

// Rungs 3 and 4, exercised through the LIVE detector this module configured — not
// through a re-declaration of its options, which would only prove the fixture matches
// itself. `i18n.services.languageDetector` IS the configured instance, so a change to
// `detection.order` or to `supportedLngs` moves these.
describe('browser + fallback rungs (ADR-066 rungs 3 and 4)', () => {
  function withNavigatorLanguages(langs: string[]) {
    // jsdom exposes navigator.languages as a prototype getter, so it is spy-able;
    // defineProperty is the fallback for a runtime where it is a own-value property.
    try {
      vi.spyOn(window.navigator, 'languages', 'get').mockReturnValue(langs);
    } catch {
      Object.defineProperty(window.navigator, 'languages', {
        value: langs,
        configurable: true,
      });
    }
  }

  function detected(): string[] {
    const raw = i18n.services.languageDetector?.detect();
    if (!raw) return [];
    return Array.isArray(raw) ? raw : [raw];
  }

  // Rung 3: with no explicit choice stored, the browser's advertised languages decide.
  // Delete 'navigator' from detection.order and this reddens.
  it('detects the browser language when no explicit choice is stored', async () => {
    withNavigatorLanguages(['es-MX', 'es']);
    expect(detected().some((l) => l.startsWith('es'))).toBe(true);

    await i18n.changeLanguage(detected()[0]);
    // nonExplicitSupportedLngs folds the region variant onto the shipped base catalog
    // rather than dropping straight to English.
    expect(i18n.resolvedLanguage).toBe('es');
  });

  // Rung 1 still outranks rung 3 at the DETECTOR, not just in the seam: the stored
  // choice must be found first in detection.order.
  it('puts an explicit stored choice ahead of the browser', () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'en');
    withNavigatorLanguages(['es-MX', 'es']);
    expect(detected()[0]).toBe('en');
  });

  // Rung 4: a browser language whose catalog the console does not ship must land on
  // English, never on a missing catalog. supportedLngs + fallbackLng is what does it;
  // widening supportedLngs past the shipped registry would break this.
  it('falls back to English for a language the console does not ship', async () => {
    withNavigatorLanguages(['fr-FR', 'fr']);
    await i18n.changeLanguage(detected()[0]);
    expect(i18n.resolvedLanguage).toBe(DEFAULT_LOCALE);
    expect(DEFAULT_LOCALE).toBe('en');
  });
});

// Spanish is ADR-066's proof locale (sub-workstream c). These pin that the broad
// `es` corpus actually resolves — a missing/misnamed catalog would fall back to the
// English string (or the raw key) and redden here rather than in a live demo.
describe('Spanish (es) proof locale', () => {
  it('is shipped and selectable via the switcher', () => {
    expect(SUPPORTED_LOCALES.map((l) => l.code)).toContain('es');
    setUserLocale('es');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('es');
  });

  it('resolves the es catalog across the chrome and screen namespaces', async () => {
    await i18n.changeLanguage('es');
    expect(i18n.t('nav:devices')).toBe('Dispositivos');
    expect(i18n.t('nav:alarms')).toBe('Alarmas');
    expect(i18n.t('userMenu:signOut')).toBe('Cerrar sesión');
    expect(i18n.t('theme:dark')).toBe('Oscuro');
    expect(i18n.t('devices:empty')).toBe('Aún no hay dispositivos registrados.');
    expect(i18n.t('tenants:title')).toBe('Inquilinos');
    expect(i18n.t('login:signIn')).toBe('Iniciar sesión');
  });

  it('selects the correct plural category for the pagination line in both locales', async () => {
    // The pagination count is the corpus's one plural (i18next _one/_other, driven
    // by `count`). Pin one and many in each locale so a dropped plural form (which
    // silently renders the _other string for count===1) reddens.
    await i18n.changeLanguage('en');
    expect(i18n.t('common:paginationShowing', { start: 1, end: 1, count: 1 })).toBe(
      'Showing 1–1 of 1 result',
    );
    expect(i18n.t('common:paginationShowing', { start: 1, end: 20, count: 42 })).toBe(
      'Showing 1–20 of 42 results',
    );
    await i18n.changeLanguage('es');
    expect(i18n.t('common:paginationShowing', { start: 1, end: 1, count: 1 })).toBe(
      'Mostrando 1–1 de 1 resultado',
    );
    expect(i18n.t('common:paginationShowing', { start: 1, end: 20, count: 42 })).toBe(
      'Mostrando 1–20 de 42 resultados',
    );
  });
});
