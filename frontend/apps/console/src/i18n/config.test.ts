// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment jsdom
//
// jsdom, not the repo-default node env: config.ts initializes the browser
// language detector and the seam functions touch localStorage/window.

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
});
