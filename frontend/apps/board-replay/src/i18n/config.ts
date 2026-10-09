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

import enChrome from './locales/en/chrome.json';
import esChrome from './locales/es/chrome.json';
import zhChrome from './locales/zh-CN/chrome.json';

export const SUPPORTED_LOCALES = ['en', 'es', 'zh-CN'] as const;
export type ReplayLocale = (typeof SUPPORTED_LOCALES)[number];
export const DEFAULT_LOCALE: ReplayLocale = 'en';

export const RESOURCES = {
  en: { chrome: enChrome },
  es: { chrome: esChrome },
  'zh-CN': { chrome: zhChrome },
} as const;

// resolveLocale maps a host-supplied tag ("es", "es-MX", "zh", "zh-Hans-CN") onto a
// supported locale, falling back to English.
export function resolveLocale(tag: string | null | undefined): ReplayLocale {
  const t = (tag ?? '').trim().toLowerCase();
  if (t === '') return DEFAULT_LOCALE;
  if (t === 'zh' || t.startsWith('zh-')) return 'zh-CN';
  return t.split('-')[0] === 'es' ? 'es' : 'en';
}

void i18n.use(initReactI18next).init({
  resources: RESOURCES,
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

export async function setReplayLocale(tag: string | null | undefined): Promise<ReplayLocale> {
  const locale = resolveLocale(tag);
  await i18n.changeLanguage(locale);
  // 🔴 Deliberately does NOT write <html lang>: the page that embeds the replay owns it.
  // The language is declared on the replay's own root element (App.tsx).
  return locale;
}

export default i18n;
