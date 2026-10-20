// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 A SEPARATE FILE BECAUSE THE THING UNDER TEST HAPPENS AT IMPORT TIME, ONCE.
//
// config.ts keeps `<html lang>` in step two ways: a `languageChanged` listener, and one
// direct call right after `init()`. config.test.ts covers the listener — every one of its
// assertions goes through a `changeLanguage`, including the one in its `beforeEach`. So
// the direct call would be covered by NOTHING: deleting it leaves that whole suite green.
//
// Two startup paths, one test each:
//   - a language whose catalogs are bundled (English) resolves synchronously inside
//     `init()`, and its `languageChanged` is emitted BEFORE the listener is registered on
//     the next line, so only the direct call can announce it;
//   - a language loaded on demand (Spanish) resolves later, once its chunk is in, and the
//     listener announces it. This one runs the REAL lazy loader, not the preloaded bundles
//     vitest.setup.ts registers for every other suite.
//
// A test inside config.test.ts could not see either: the setup file has already imported
// config.ts by then, so the init has run. Hence a file that resets the module registry and
// watches a fresh import happen.

import { beforeEach, describe, expect, it, vi } from 'vitest';

// 🔴 THE STORAGE KEY IS WRITTEN OUT RATHER THAN IMPORTED, AND THAT IS FORCED. Importing it
// from ./config would evaluate config.ts at module-load time — hoisted above the setItem
// below — and the init this file exists to observe would already have happened against an
// empty localStorage. So there is no import from ./config anywhere here.
//
// The duplicated literal is anchored, not loose: config.test.ts asserts
// `LOCALE_STORAGE_KEY === 'dc.locale'`, so a rename reddens there and names the string to
// change here.
const LOCALE_KEY = 'dc.locale';

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
});

describe('the document language is correct before anything calls changeLanguage', () => {
  it('takes the locale i18next resolved at init, not index.html’s static lang', async () => {
    // index.html ships lang="en". Seeding something that is neither that nor the expected
    // answer means a PASS cannot come from the static default happening to be right, nor
    // from jsdom's own initial value.
    document.documentElement.lang = 'zz-untouched';
    localStorage.setItem(LOCALE_KEY, 'en');

    await import('./config');

    // Nothing has awaited anything: 'zz-untouched' means the direct call is gone.
    expect(document.documentElement.lang).toBe('en');
  });

  it('follows a locale that loads on demand once its catalogs arrive', async () => {
    document.documentElement.lang = 'zz-untouched';

    // Written BEFORE the import, which is the whole point: config.ts reads this key
    // through the language detector while it initializes.
    localStorage.setItem(LOCALE_KEY, 'es');

    const { i18nReady, default: i18n } = await import('./config');
    await i18nReady;

    expect(i18n.resolvedLanguage).toBe('es');
    expect(i18n.t('back', { ns: 'common' })).not.toBe('back');
    expect(document.documentElement.lang).toBe('es');
  });
});
