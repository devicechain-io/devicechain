// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// i18next for the replay page's CHROME: the header, transport bar, provenance panel and
// error states. Widget-internal text and the board's authored titles are not part of it;
// they are English, and the provenance panel carries one translated line saying so.
//
// The locale comes from the HOST PAGE (data-locale on the mount element, else <html lang>),
// never from a stored choice or the browser: the page the viewer is on already chose a
// language, and a replay that disagreed with its own page would be wrong in a way nobody
// asked for.

import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

// Only English is in the first download: it is the default and the fallback for every key.
// The other catalogs are fetched by setReplayLocale, which mount() awaits before it renders
// anything, so a Spanish or Chinese page still draws its first frame in its own language.
import enChrome from './locales/en/chrome.json';

export const SUPPORTED_LOCALES = ['en', 'es', 'zh-CN'] as const;
export type ReplayLocale = (typeof SUPPORTED_LOCALES)[number];
export const DEFAULT_LOCALE: ReplayLocale = 'en';

type Catalog = Record<string, string>;

// One loader per locale. The `import()` calls are literals so the bundler splits each catalog
// into a chunk of its own; English resolves from the entry and never waits on the network.
export const CATALOG_LOADERS: Record<ReplayLocale, () => Promise<Catalog>> = {
  en: () => Promise.resolve(enChrome),
  es: () => import('./locales/es/chrome.json').then((m) => m.default),
  'zh-CN': () => import('./locales/zh-CN/chrome.json').then((m) => m.default),
};

// resolveLocale maps a host-supplied tag ("es", "es-MX", "zh", "zh-Hans-CN") onto a
// supported locale, falling back to English.
export function resolveLocale(tag: string | null | undefined): ReplayLocale {
  const t = (tag ?? '').trim().toLowerCase();
  if (t === '') return DEFAULT_LOCALE;
  if (t === 'zh' || t.startsWith('zh-')) return 'zh-CN';
  return t.split('-')[0] === 'es' ? 'es' : 'en';
}

void i18n.use(initReactI18next).init({
  resources: { [DEFAULT_LOCALE]: { chrome: enChrome } },
  lng: DEFAULT_LOCALE,
  fallbackLng: DEFAULT_LOCALE,
  supportedLngs: [...SUPPORTED_LOCALES],
  ns: ['chrome'],
  defaultNS: 'chrome',
  // Flat semantic keys: `.` is a literal key character, not a path separator.
  keySeparator: false,
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
});

// The catalog for `locale`, fetched once and registered with i18next.
export async function loadReplayCatalog(locale: ReplayLocale): Promise<void> {
  if (i18n.hasResourceBundle(locale, 'chrome')) return;
  i18n.addResourceBundle(locale, 'chrome', await CATALOG_LOADERS[locale]());
}

export async function setReplayLocale(tag: string | null | undefined): Promise<ReplayLocale> {
  let locale = resolveLocale(tag);
  try {
    await loadReplayCatalog(locale);
  } catch (err) {
    // A catalog that cannot be fetched leaves the replay in English, the language every key
    // falls back to, rather than declaring a language its text is not in.
    console.warn(`board-replay: the ${locale} catalog could not be loaded; showing English`, err);
    locale = DEFAULT_LOCALE;
  }
  await i18n.changeLanguage(locale);
  // 🔴 Deliberately does NOT write <html lang>: the page that embeds the replay owns it.
  // The language is declared on the replay's own root element (App.tsx).
  return locale;
}

export default i18n;
