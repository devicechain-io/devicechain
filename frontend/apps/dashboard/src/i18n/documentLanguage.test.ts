// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 A SEPARATE FILE BECAUSE THE THING UNDER TEST HAPPENS AT IMPORT TIME, ONCE.
//
// config.ts keeps `<html lang>` in step two ways: a `languageChanged` listener, and one
// direct call right after `init()`. config.test.ts covers the listener — every one of
// its assertions goes through a `changeLanguage`, including the one in its `beforeEach`.
// So the direct call was covered by NOTHING: deleting it left the suite at 65/65 green.
//
// That is the half that serves the case this app's localization leads with. i18next
// resolves the language synchronously inside `init()` when resources are bundled, and
// emits `languageChanged` BEFORE the listener is registered on the next line. A viewer
// whose browser resolves `es` and who never touches the switcher therefore gets the
// right `lang` only from the direct call — delete it and every such viewer reads a
// Spanish page announced to their screen reader as English, for the whole session, with
// the test suite green.
//
// A test inside config.test.ts could not have caught it: vitest isolates MODULES PER
// FILE, so once that file imports config.ts the init has already run and cannot be
// observed. Hence a file whose only job is to watch the import happen.

import { describe, expect, it } from 'vitest';

// 🔴 THE STORAGE KEY IS WRITTEN OUT RATHER THAN IMPORTED, AND THAT IS FORCED. Importing
// it from ./config would evaluate config.ts at module-load time — hoisted above the
// setItem below — and the init this file exists to observe would already have happened
// against an empty localStorage. So there is no import from ./config anywhere here.
//
// The duplicated literal is anchored, not loose: config.test.ts asserts
// `LOCALE_STORAGE_KEY === 'dc.locale'`, so a rename reddens there and names the string
// to change here.
const LOCALE_KEY = 'dc.locale';

describe('the document language is correct before anything calls changeLanguage', () => {
  it('takes the locale i18next resolved at init, not index.html’s static lang', async () => {
    // index.html ships lang="en". Seeding something that is neither that nor the
    // expected answer means a PASS cannot come from the static default happening to be
    // right, nor from jsdom's own initial value.
    document.documentElement.lang = 'zz-untouched';

    // Written BEFORE the import, which is the whole point: config.ts reads this key
    // through the language detector while it initializes.
    localStorage.setItem(LOCALE_KEY, 'es');

    await import('./config');

    // No changeLanguage has run in this file. 'zz-untouched' means the direct call is
    // gone; 'en' means the detector never saw the stored choice.
    expect(document.documentElement.lang).toBe('es');
  });
});
