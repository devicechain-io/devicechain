// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// i18next initialization for the /dash viewer (ADR-066, extended to the second app).
//
// 🔴 THIS APP HAD NO i18n AT ALL until this file existed — no i18next dependency,
// no catalogs, no lint. That is worth stating rather than quietly fixing, because
// the reason it happened is structural: /dash is a separate app with its own login
// and its own render tree, so every piece of chrome the console localized left this
// surface untouched, and the CI gate that would have said so was scoped to
// apps/console by a `--workspaces --if-present` that only the console answered. The
// same shape produced the tenant-basemap miss recorded in queries.ts: work done "in
// the console" is not work done in the product.
//
// The shape here deliberately MIRRORS apps/console/src/i18n/config.ts — same locale
// registry, same storage key, same precedence, same `applyTenantDefaultLocale` seam
// — so a tenant-default locale delivered on the ADR-038 white-label cascade can be
// adopted here later with no second design. It is a COPY rather than a shared module on
// purpose: the two apps ship independently (the console is the authoring app; this
// one is the reference external embedder a third party clones), and a shared runtime
// module would make the embedder depend on console internals it exists to
// demonstrate living without.

import i18n from 'i18next';
import LanguageDetector from 'i18next-browser-languagedetector';
import { initReactI18next } from 'react-i18next';

import enCommon from './locales/en/common.json';
import enLoad from './locales/en/load.json';
import enSignIn from './locales/en/signIn.json';
import enView from './locales/en/view.json';

import esCommon from './locales/es/common.json';
import esLoad from './locales/es/load.json';
import esSignIn from './locales/es/signIn.json';
import esView from './locales/es/view.json';

export interface Locale {
  /** BCP-47 code; also the ./locales/<code>/ directory name and the i18next lng. */
  code: string;
  /** The language's own endonym, shown in the switcher — never itself translated. */
  label: string;
  /**
   * A short language-code badge shown before the endonym in the switcher ("EN",
   * "ES") — a code rather than a flag, because a flag denotes a country and not a
   * language. Set explicitly so an ambiguous split (pt-BR vs pt-PT) can be
   * disambiguated.
   */
  badge: string;
}

// The locales /dash ships. This MUST stay in step with the console's list: the two
// apps are two views of one product, and a locale offered in one and missing in the
// other is a viewer who can read the console but not the board embedded from it.
// `supportedLngs` below is derived from this, so browser detection can never resolve
// to a locale whose catalog is absent (which would render raw keys to the viewer).
export const SUPPORTED_LOCALES: Locale[] = [
  { code: 'en', label: 'English', badge: 'EN' },
  { code: 'es', label: 'Español', badge: 'ES' },
];

export const DEFAULT_LOCALE = 'en';

// localStorage key holding an EXPLICIT viewer locale choice. THE SAME KEY THE
// CONSOLE USES, deliberately: /dash is served same-origin at /dash behind the shared
// ingress, so the two apps share a localStorage origin, and a viewer who picked
// Spanish in the console should not have to pick it again here. Only the switcher
// writes it (see setUserLocale), which is what lets the tenant-default seam below
// read it as an unambiguous "the user chose".
export const LOCALE_STORAGE_KEY = 'dc.locale';

// One namespace per step of the viewer, so a catalog maps to a screen: `signIn`
// (step 1), `load` (step 2, including every message loadDashboard can produce),
// `view` (step 3), and `common` for the chrome all three share.
export const NAMESPACES = ['common', 'signIn', 'load', 'view'] as const;

const resources = {
  en: { common: enCommon, signIn: enSignIn, load: enLoad, view: enView },
  es: { common: esCommon, signIn: esSignIn, load: esLoad, view: esView },
} as const;

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: DEFAULT_LOCALE,
    supportedLngs: SUPPORTED_LOCALES.map((l) => l.code),
    // Fall a region variant back onto its base language ("es-MX" -> "es") rather
    // than straight to `en`, so a future regional catalog is reachable.
    nonExplicitSupportedLngs: true,
    ns: NAMESPACES,
    defaultNS: 'common',
    // Keys are flat semantic identifiers, never a dotted tree — so `.` is a literal
    // key character, not a path separator. `:` stays the namespace separator.
    keySeparator: false,
    nsSeparator: ':',
    interpolation: {
      // React escapes rendered values already; i18next must not double-escape.
      escapeValue: false,
    },
    detection: {
      // (1) an explicit user choice in localStorage, then (2) the browser's
      // languages, then (3) the `en` fallbackLng. A tenant default sits between 1
      // and 2 and is NOT in this array on purpose — `applyTenantDefaultLocale`
      // below expresses that precedence directly. `caches: []` keeps the detector
      // from writing LOCALE_STORAGE_KEY, so that key stays a pure "user chose".
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: LOCALE_STORAGE_KEY,
      caches: [],
    },
    react: {
      // Resources are bundled (no async load), so a Suspense boundary would only
      // add a needless fallback flash.
      useSuspense: false,
    },
  });

/**
 * Keep `<html lang>` in step with the language actually in effect.
 *
 * 🔴 NOT DECORATION. The document language is what a screen reader picks its voice
 * and pronunciation rules from, and what a browser offers "translate this page"
 * against; a Spanish page still declaring `lang="en"` is read aloud in an English
 * voice. index.html ships `lang="en"` as the static default, so without this the
 * attribute would be a lie for every viewer whose browser resolved `es`.
 *
 * Registered as a listener AND applied once, because the language i18next resolves
 * at init is settled before any component mounts and so emits no later event.
 */
function syncDocumentLanguage(): void {
  if (typeof document === 'undefined') return;
  document.documentElement.lang = i18n.resolvedLanguage ?? DEFAULT_LOCALE;
}
i18n.on('languageChanged', syncDocumentLanguage);
syncDocumentLanguage();

/**
 * Persist an explicit viewer locale choice and switch to it. The switcher calls
 * this; it is the ONLY writer of LOCALE_STORAGE_KEY, which is what lets
 * `applyTenantDefaultLocale` treat that key as an unambiguous "user has chosen"
 * signal. Silently ignores a locale /dash does not ship.
 */
export function setUserLocale(code: string): void {
  if (!SUPPORTED_LOCALES.some((l) => l.code === code)) return;
  localStorage.setItem(LOCALE_STORAGE_KEY, code);
  void i18n.changeLanguage(code);
}

/**
 * The seam for a tenant-default locale delivered on the ADR-038 white-label cascade. The
 * viewer will call this once it knows the tenant's default; the precedence contract
 * lives here so a later slice cannot get it subtly wrong. It is a NO-OP when the
 * viewer has already made an explicit choice (rung 1 beats rung 2) and ignores a
 * locale we do not ship — so a tenant default only ever fills in for a viewer who
 * has not chosen, and only with a shipped locale.
 */
export function applyTenantDefaultLocale(locale: string | null | undefined): void {
  if (!locale) return;
  // An explicit user choice wins — but only a choice that still resolves to a
  // shipped locale. A stale/unshipped stored value has no runtime effect (detection
  // ignores it), so it must NOT block an effective tenant default; treating any
  // string as "chosen" would let an ineffective rung-1 suppress an effective rung-2.
  const chosen = localStorage.getItem(LOCALE_STORAGE_KEY);
  if (chosen && isShippedLocale(chosen)) return;
  if (!isShippedLocale(locale)) return; // never select a locale that would render raw keys
  void i18n.changeLanguage(locale);
}

/**
 * Whether a language tag resolves to a catalog this build actually ships.
 *
 * 🔴 IT ASKS i18next RATHER THAN RE-DERIVING THE ANSWER, and that is the whole point. The
 * question "would this tag render one of our catalogs?" has exactly one correct answer —
 * the one `changeLanguage` is about to act on — and any second implementation of it is a
 * source of disagreement rather than a convenience.
 *
 * This seam was an EXACT match against SUPPORTED_LOCALES, which disagreed with
 * `changeLanguage` in both directions at once. `nonExplicitSupportedLngs` is on, so a
 * browser advertising `es-MX` renders Spanish while a TENANT DEFAULT of `es-MX` was
 * declared unshipped and left the viewer on English — the same tag, two answers,
 * depending only on which rung supplied it. The console's identical seam then grew a
 * hand-rolled fold to cover the regional case (`code === tag || code.toLowerCase() ===
 * base`) and got the other direction wrong: it lowercased where i18next does not, so `ES`
 * was declared resolvable, handed to `changeLanguage`, and rendered `en`. Both are gone
 * for the same reason — a fold that has now been wrong twice does not get a third copy.
 *
 * `isSupportedCode` applies `nonExplicitSupportedLngs` itself, so the regional fold comes
 * for free and stays correct if that option ever changes. Measured, not assumed — `es-MX`,
 * `es-mx`, `es-419` and `es-Latn-MX` are supported and render Spanish; `ES`, `pt-BR`,
 * `fr-CA` and `zh-Hans-CN` are not and render English.
 *
 * 🔴 ONE CLASS OF TAG WHERE THE TWO STILL DISAGREE, measured the same way and recorded
 * because "asks i18next" reads like it cannot: `ES-MX`, `eS-mx` and `ES-419` are answered
 * NO here and rendered as Spanish by changeLanguage. It is i18next's own seam —
 * isSupportedCode splits the tag before formatting it, while the resolve hierarchy
 * canonicalises the whole tag first — not a fold of ours. The disagreement is FAIL-CLOSED
 * (a tag that would have worked is refused, never a tag that would render raw keys) and no
 * browser sends an upper-cased language subtag. Left as is rather than papered over with a
 * second normalisation, which is the thing this function exists to not have.
 *
 * The full tag is still what gets passed to changeLanguage, never a folded base: i18next
 * does the folding, so the day an `es-MX` catalog ships it is picked up with no change
 * here.
 */
export function isShippedLocale(tag: string): boolean {
  // services.languageUtils exists once init() has run, and init is synchronous here
  // (catalogs are bundled, no async backend). Guarded anyway, and fail-CLOSED: an
  // unanswerable question must not select a language.
  const utils = i18n.services?.languageUtils;
  return utils ? utils.isSupportedCode(tag) : false;
}

export default i18n;
