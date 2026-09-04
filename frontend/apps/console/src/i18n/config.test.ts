// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts): config.ts initializes the
// browser language detector and the seam functions touch localStorage/window.

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import i18n, {
  SUPPORTED_LOCALES,
  isShippedLocale,
  resetToDetectedLocale,
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

  // 🔴 THE LITERAL, PINNED. Two other places write this exact string out rather than
  // importing it, and both are forced to: documentLanguage.test.ts must seed the key
  // BEFORE config.ts is evaluated (an import would hoist the evaluation above the write
  // and defeat the test), and /dash carries its own copy because the two apps share a
  // localStorage origin behind the shared ingress and a viewer who picked Spanish in the
  // console should not have to pick it again there. A rename that only touched this file
  // would silently strand both. It reddens here instead, naming the string to change.
  it('is keyed on the exact string /dash and the init-time test also write out', () => {
    expect(LOCALE_STORAGE_KEY).toBe('dc.locale');
  });
});

// 🔴 The document language is what a screen reader picks its voice from. index.html
// ships a static lang="en", so nothing but this sync makes it true for a Spanish user —
// and nothing else in the app or the build would notice it staying wrong.
//
// This describe covers the LISTENER half only. Every assertion here runs after a
// changeLanguage, so it passes with the init-time call deleted; documentLanguage.test.ts
// is the other half, and it has to be its own file to be one.
describe('document language', () => {
  it('follows the language actually in effect', async () => {
    await i18n.changeLanguage('es');
    expect(document.documentElement.lang).toBe('es');
    await i18n.changeLanguage('en');
    expect(document.documentElement.lang).toBe('en');
  });

  it('reports the RESOLVED language, not the raw request', async () => {
    // A regional variant falls back to its base catalog (nonExplicitSupportedLngs), so
    // the page is rendering Spanish and must say so. Announcing 'es-MX' here would be
    // announcing a catalog that does not exist.
    await i18n.changeLanguage('es-MX');
    expect(document.documentElement.lang).toBe('es');
  });

  it('follows the tenant default and the revert that ends it', async () => {
    // The two seams that change the language without anyone touching the switcher. A
    // listener wired to the switcher alone would leave both of these announcing the
    // previous language.
    applyTenantDefaultLocale('es');
    await Promise.resolve();
    expect(document.documentElement.lang).toBe('es');

    resetToDetectedLocale();
    await Promise.resolve();
    expect(document.documentElement.lang).toBe(DEFAULT_LOCALE);
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

  // 🔴 A REGIONAL TAG MUST RESOLVE, because rung 3 already does. i18next's
  // nonExplicitSupportedLngs folds `es-MX` onto the `es` catalog, so a browser
  // advertising it gets Spanish; an exact-match guard here would make the tenant rung
  // the one rung that refused it, and the same user would get English from a tenant
  // default of `es-MX` while getting Spanish from their browser.
  it('accepts a regional tag whose base language is shipped', async () => {
    applyTenantDefaultLocale('es-MX');
    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });

  // The full tag is what reaches i18next, not the folded base — so the day an es-MX
  // catalog ships it is selected without a change here.
  it('hands i18next the full tag rather than the folded base language', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('es-MX');
    expect(spy).toHaveBeenCalledWith('es-MX');
  });

  // The counterweight: folding must not turn into "accept anything". A regional tag
  // on an UNSHIPPED base language is still refused.
  it('still refuses a regional tag whose base language is not shipped', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('fr-CA');
    expect(spy).not.toHaveBeenCalled();
  });

  // 🔴 CASE. i18next case-normalizes only the REGION half of a hyphenated tag —
  // `lowerCaseLng` is off — so `ES` is NOT a supported code and renders English. A
  // hand-rolled `.toLowerCase()` fold declared it resolvable, handed it to
  // changeLanguage, and the seam selected a language that renders raw keys by its own
  // definition. Unreachable through the wired path (the server canonicalizes) but a
  // contract this function states about itself.
  it('does not claim an upper-cased language is resolvable, because i18next does not', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('ES');
    expect(spy).not.toHaveBeenCalled();
  });

  // The other half of the same measurement, and the reason the fold cannot simply be
  // dropped: i18next DOES case-normalize the region, so `es-mx` is supported.
  it('accepts a lower-cased region, because i18next does', async () => {
    applyTenantDefaultLocale('es-mx');
    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });
});

// 🔴 isShippedLocale must agree with changeLanguage on EVERY tag, because disagreement
// is the whole defect class: the guard says "this renders" and the renderer says
// otherwise. Asserted as a property over a table rather than case by case, so a future
// i18next option change (lowerCaseLng, load:'languageOnly', a new supportedLng) moves
// both sides together or reddens.
describe('isShippedLocale agrees with what i18next actually resolves', () => {
  // 🔴 NO TAG WHOSE BASE LANGUAGE IS `en` AND WHICH IS UNSUPPORTED CAN APPEAR HERE,
  // and that is a limit of the ORACLE rather than a gap in the guard. Every
  // unsupported tag lands on `en` through fallbackLng, so for `En` the two outcomes
  // this test distinguishes — "matched its own catalog" and "fell back to English" —
  // are the same observation. `ES` is the same misspelling shape and IS observable,
  // because its base is `es`, so the case property is still covered; the dedicated
  // upper-case test above covers `ES` directly.
  const TAGS = [
    'en', 'es', 'ES',
    'es-MX', 'es-mx', 'es-419', 'es-Latn-MX',
    'pt-BR', 'fr', 'fr-CA', 'zh-Hans-CN',
  ];

  it('reports shipped exactly when the tag renders its own language', async () => {
    for (const tag of TAGS) {
      const claimed = isShippedLocale(tag);
      await i18n.changeLanguage(tag);
      const rendered = i18n.resolvedLanguage;
      // "Renders its own language" means the catalog i18next landed on is the tag's
      // own primary subtag — not merely that it landed somewhere. Every unsupported
      // tag lands on `en` via fallbackLng, so comparing against "did it resolve?"
      // would call `fr` a success.
      const base = tag.split(/[-_]/)[0].toLowerCase();
      const renderedOwnLanguage = rendered?.toLowerCase() === base;
      expect(claimed, `${tag}: guard said ${claimed}, i18next rendered ${rendered}`).toBe(
        renderedOwnLanguage,
      );
    }
  });

  it('loses to an explicit user choice already in localStorage', () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'en');
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('en');
    expect(spy).not.toHaveBeenCalled();
  });

  // 🔴 THE STORED SIDE OF THE SAME QUESTION. The guard asks isShippedLocale about the
  // stored value too, so it has to get the same answer there: `es-MX` DOES render
  // Spanish, so it is an effective rung-1 and must block rung 2. An exact match against
  // SUPPORTED_LOCALES calls it neither shipped nor stale, and the viewer is moved off a
  // language they had picked.
  it('is blocked by a stored regional variant, which is an effective choice', () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'es-MX');
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

  // 🔴 resetToDetectedLocale hands the language back to the rungs that do not need a
  // tenant. TenantProvider calls it on unmount so a tenant default does not outlive the
  // tenant — without it, logging out of a Spanish-default tenant into one with no
  // default leaves the console in Spanish against an English browser.
  //
  // These pin that it RE-RUNS DETECTION rather than picking a constant: the same call
  // lands on a different language for each of the three rungs below.
  it('reverts to the browser language', async () => {
    withNavigatorLanguages(['es-MX', 'es']);
    await i18n.changeLanguage('en');

    resetToDetectedLocale();

    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });

  it('reverts to an explicit user choice ahead of the browser', async () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'es');
    withNavigatorLanguages(['en-US', 'en']);
    await i18n.changeLanguage('en');

    resetToDetectedLocale();

    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });

  it('reverts to English when the browser asks for a language we do not ship', async () => {
    withNavigatorLanguages(['fr-FR', 'fr']);
    await i18n.changeLanguage('es');

    resetToDetectedLocale();

    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('en'));
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
