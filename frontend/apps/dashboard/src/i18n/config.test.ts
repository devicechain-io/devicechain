// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vitest.config.ts): config.ts initializes the
// browser language detector, the seam functions touch localStorage, and the document
// language sync touches window.document.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

import i18n, {
  applyTenantDefaultLocale,
  DEFAULT_LOCALE,
  isShippedLocale,
  LOCALE_STORAGE_KEY,
  NAMESPACES,
  setUserLocale,
  SUPPORTED_LOCALES,
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
    expect(i18n.t('signIn:title')).toBe('Sign in');
    expect(i18n.t('load:render')).toBe('Render');
    // Cross-namespace lookup: the default namespace is `common`.
    expect(i18n.t('common:signOut')).toBe('Sign out');
    expect(i18n.t('signOut')).toBe('Sign out');
  });

  it('falls back to the key for a missing string rather than crashing', () => {
    expect(i18n.t('load:doesNotExist')).toBe('doesNotExist');
  });

  it('only advertises locales it actually ships', () => {
    // supportedLngs is derived from the registry, so the detector can never resolve to
    // a locale whose catalog is absent. i18next appends 'cimode'.
    const supported = (i18n.options.supportedLngs || []).filter((l) => l !== 'cimode');
    expect(supported).toEqual(SUPPORTED_LOCALES.map((l) => l.code));
    expect(SUPPORTED_LOCALES.map((l) => l.code)).toContain(DEFAULT_LOCALE);
  });

  it('registers every namespace the app declares', () => {
    // A namespace missing from `ns` resolves to nothing at runtime even though its
    // catalogs are present and the parity test is happy about them.
    for (const ns of NAMESPACES) {
      expect(i18n.options.ns, `namespace ${ns} is not registered with i18next`).toContain(ns);
    }
  });

  it('every shipped locale carries a switcher label and code badge', () => {
    // The switcher renders the `badge` chip + `label` per locale; a blank either would
    // show an empty pill. Endonym labels are never translated, so pin they exist here.
    for (const l of SUPPORTED_LOCALES) {
      expect(l.label.length).toBeGreaterThan(0);
      expect(l.badge.length).toBeGreaterThan(0);
    }
  });

  it('never writes the user-choice key on a plain language change (caches: [])', async () => {
    // Directly pins detection.caches === []: only the switcher (setUserLocale) may
    // write LOCALE_STORAGE_KEY, so its presence stays an unambiguous "user chose"
    // signal for the tenant-default seam. If caches reverted to the detector default
    // ['localStorage'], changeLanguage would cache the language here and this reddens.
    await i18n.changeLanguage('en');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });

  it('shares the console’s storage key, so one choice covers both apps', () => {
    // /dash is served same-origin at /dash behind the shared ingress. Changing this
    // string here without changing it in apps/console makes a viewer choose twice.
    expect(LOCALE_STORAGE_KEY).toBe('dc.locale');
  });
});

// 🔴 The document language is what a screen reader picks its voice from. index.html
// ships a static lang="en", so nothing but this sync makes it true for a Spanish
// viewer — and nothing else in the app or the build would notice it staying wrong.
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
});

describe('setUserLocale', () => {
  it('persists a shipped locale as the explicit user choice and switches to it', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    setUserLocale('es');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('es');
    expect(spy).toHaveBeenCalledWith('es');
  });

  it('ignores a locale the viewer does not ship', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    setUserLocale('fr');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
    expect(spy).not.toHaveBeenCalled();
  });
});

// The precedence contract a tenant-default locale inherits: it only fills in for a
// viewer who has not chosen, and only with a shipped locale.
describe('applyTenantDefaultLocale (the tenant-default seam)', () => {
  it('is a no-op for an empty/absent tenant default', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale(null);
    applyTenantDefaultLocale(undefined);
    applyTenantDefaultLocale('');
    expect(spy).not.toHaveBeenCalled();
  });

  it('does not select a locale the viewer does not ship', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('fr');
    expect(spy).not.toHaveBeenCalled();
  });

  it('loses to an explicit user choice already in localStorage', () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'en');
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('es');
    expect(spy).not.toHaveBeenCalled();
  });

  // 🔴 THE SAME WIDENING, ON THE OTHER SIDE OF THE GUARD. A stored choice is checked with
  // the same question, so it has to get the same answer: `es-MX` DOES render Spanish, so
  // it is an effective rung-1 and must block rung 2. Under an exact match it was neither
  // — refused as a tenant default AND ignored as a choice — so the viewer would be moved
  // off a language they had picked.
  it('is blocked by a stored regional variant, which is an effective choice', () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'es-MX');
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('en');
    expect(spy).not.toHaveBeenCalled();
  });

  it('is NOT blocked by a stored choice that no longer resolves to a shipped locale', () => {
    // A stale/unshipped stored value has no runtime effect, so an effective tenant
    // default must still apply — an ineffective rung-1 must not suppress an effective
    // rung-2. Deleting the stored-value validation in the guard reddens this.
    localStorage.setItem(LOCALE_STORAGE_KEY, 'xx-not-shipped');
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('es');
    expect(spy).toHaveBeenCalledWith('es');
  });

  it('applies a shipped tenant default when the viewer has made no choice', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('es');
    expect(spy).toHaveBeenCalledWith('es');
  });

  // 🔴 THE TAG THE OLD EXACT-MATCH GUARD DROPPED. `nonExplicitSupportedLngs` is on, so a
  // BROWSER advertising es-MX renders Spanish; an exact match against SUPPORTED_LOCALES
  // declared the same tag unshipped when a TENANT sent it and left the viewer on English.
  // One tag, two answers, decided by which rung supplied it.
  it('accepts a regional variant of a shipped locale, because i18next resolves one', async () => {
    applyTenantDefaultLocale('es-MX');
    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });

  // 🔴 THE OTHER DIRECTION, and the reason the fold is not simply widened to a
  // case-insensitive compare. i18next case-normalizes the REGION half of a tag and not
  // the language half, so `ES` resolves to `en` — declaring it shipped would have the
  // seam SELECTING a language that renders the English catalog while claiming Spanish.
  it('refuses a mis-cased language subtag, because i18next would render English', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage');
    applyTenantDefaultLocale('ES');
    expect(spy).not.toHaveBeenCalled();
  });

  // The other half of the same measurement: i18next DOES case-normalize the region.
  it('accepts a lower-cased region, because i18next does', async () => {
    applyTenantDefaultLocale('es-mx');
    await vi.waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });
});

// 🔴 isShippedLocale must agree with changeLanguage on EVERY tag, because disagreement IS
// the defect class: the guard says "this renders" and the renderer says otherwise, or the
// guard refuses a tag the renderer would have handled. Asserted as a property over a table
// rather than case by case, so a future i18next option change (lowerCaseLng,
// load:'languageOnly', a new supportedLng) moves both sides together or reddens.
describe('isShippedLocale agrees with what i18next actually resolves', () => {
  // 🔴 NO TAG WHOSE BASE LANGUAGE IS `en` AND WHICH IS UNSUPPORTED CAN APPEAR HERE, and
  // that is a limit of the ORACLE rather than a gap in the guard. Every unsupported tag
  // lands on `en` through fallbackLng, so for `En` the two outcomes this test
  // distinguishes — "matched its own catalog" and "fell back to English" — are the same
  // observation. `ES` is the same misspelling shape and IS observable, because its base is
  // `es`, so the case property stays covered.
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
      // "Renders its own language" means the catalog i18next landed on is the tag's own
      // primary subtag — not merely that it landed somewhere. Every unsupported tag lands
      // on `en` via fallbackLng, so comparing against "did it resolve?" would call `fr` a
      // success.
      const base = tag.split(/[-_]/)[0].toLowerCase();
      const renderedOwnLanguage = rendered?.toLowerCase() === base;
      expect(claimed, `${tag}: guard said ${claimed}, i18next rendered ${rendered}`).toBe(
        renderedOwnLanguage,
      );
    }
  });
});

// Spanish is the proof locale. These pin that the `es` corpus actually RESOLVES — a
// missing or misnamed catalog would fall back to the English string (or to the raw
// key) and redden here rather than in front of a viewer.
describe('Spanish (es)', () => {
  it('is shipped and selectable via the switcher', () => {
    expect(SUPPORTED_LOCALES.map((l) => l.code)).toContain('es');
    setUserLocale('es');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('es');
  });

  it('resolves every screen’s namespace', async () => {
    await i18n.changeLanguage('es');
    expect(i18n.t('common:signOut')).toBe('Cerrar sesión');
    expect(i18n.t('signIn:title')).toBe('Iniciar sesión');
    expect(i18n.t('load:title')).toBe('Cargar un panel');
    expect(i18n.t('view:change')).toBe('Cambiar');
  });

  it('selects the correct plural category for the dropped-slot message in both locales', async () => {
    // The dropped-slot count is this corpus's one plural (i18next _one/_other, driven
    // by `count`). Pin one and many in each locale so a dropped plural form — which
    // silently renders the _other string for count === 1 — reddens.
    await i18n.changeLanguage('en');
    expect(i18n.t('load:errorManifestDropped', { count: 1, slots: '"zone"' })).toContain(
      '"zone" was ignored',
    );
    expect(
      i18n.t('load:errorManifestDropped', { count: 2, slots: '"zone", "sensor"' }),
    ).toContain('"zone", "sensor" were ignored');

    await i18n.changeLanguage('es');
    expect(i18n.t('load:errorManifestDropped', { count: 1, slots: '"zone"' })).toContain(
      'se ignoró "zone"',
    );
    expect(
      i18n.t('load:errorManifestDropped', { count: 2, slots: '"zone", "sensor"' }),
    ).toContain('se ignoraron "zone", "sensor"');
  });
});
