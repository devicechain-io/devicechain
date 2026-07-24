// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// i18next initialization for the console (ADR-066). This is the framework-wiring
// half of the i18n work: the provider, locale detection, and the catalog wiring.
// Importing this module for its side effect (see main.tsx) initializes the shared
// i18next instance; components consume it through react-i18next's useTranslation.
//
// Slice scope (ADR-066 sub-workstream a): English only, two namespaces, one
// converted reference screen (Login). The one-time string-externalization sweep
// (b), the Spanish catalog (c), and the tenant-default locale (d) are separate
// workstreams; the seams for each are marked below.

import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import LanguageDetector from 'i18next-browser-languagedetector';

import enCommon from './locales/en/common.json';
import enLogin from './locales/en/login.json';
import enNav from './locales/en/nav.json';
import enUserMenu from './locales/en/userMenu.json';
import enTheme from './locales/en/theme.json';
import enDevices from './locales/en/devices.json';
import enAlarms from './locales/en/alarms.json';
import enDashboards from './locales/en/dashboards.json';
import enTenants from './locales/en/tenants.json';
import enTiers from './locales/en/tiers.json';
import enConnectors from './locales/en/connectors.json';
import enBranding from './locales/en/branding.json';
import enBrowse from './locales/en/browse.json';
import enFacets from './locales/en/facets.json';
import enAudit from './locales/en/audit.json';
import enIdentities from './locales/en/identities.json';
import enRoles from './locales/en/roles.json';

import esCommon from './locales/es/common.json';
import esLogin from './locales/es/login.json';
import esNav from './locales/es/nav.json';
import esUserMenu from './locales/es/userMenu.json';
import esTheme from './locales/es/theme.json';
import esDevices from './locales/es/devices.json';
import esAlarms from './locales/es/alarms.json';
import esDashboards from './locales/es/dashboards.json';
import esTenants from './locales/es/tenants.json';
import esTiers from './locales/es/tiers.json';
import esConnectors from './locales/es/connectors.json';
import esBranding from './locales/es/branding.json';
import esBrowse from './locales/es/browse.json';
import esFacets from './locales/es/facets.json';
import esAudit from './locales/es/audit.json';
import esIdentities from './locales/es/identities.json';
import esRoles from './locales/es/roles.json';

export interface Locale {
  /** BCP-47 code; also the ./locales/<code>/ directory name and the i18next lng. */
  code: string;
  /** The language's own endonym, shown in the switcher — never itself translated. */
  label: string;
  /**
   * A short language-code badge shown as a chip before the endonym in the switcher
   * (e.g. "EN", "ES") — the quick visual anchor, chosen over a flag on purpose: a
   * flag denotes a country, not a language (Spanish spans many flags), and a code
   * chip stays unambiguous and legible as the locale list grows past a handful.
   * Usually the primary subtag uppercased; set it explicitly so an ambiguous split
   * (pt-BR vs pt-PT) can be disambiguated.
   */
  badge: string;
}

// The locales the console actually ships. Adding one is a one-line change here
// plus its catalogs under ./locales/<code>/. Spanish is ADR-066's proof locale
// (sub-workstream c): its `es` catalogs are a machine-drafted first pass pending
// native review, deliberately broad to prove the pipeline end to end (chrome +
// the primary tenant/admin screens) rather than deep. Keeping this list to
// only-shipped locales is deliberate — `supportedLngs` below is derived from it,
// so browser detection can never resolve to a locale whose catalog is missing
// (which would render raw keys to the user).
export const SUPPORTED_LOCALES: Locale[] = [
  { code: 'en', label: 'English', badge: 'EN' },
  { code: 'es', label: 'Español', badge: 'ES' },
];

export const DEFAULT_LOCALE = 'en';

// localStorage key holding an EXPLICIT user locale choice — ADR-066 precedence
// rung 1. Only the locale switcher writes it (see LocaleSwitcher), so its
// presence unambiguously means "the user chose this," distinct from a language
// the detector merely inferred from the browser. That distinction is why the
// detector is configured with `caches: []` (it never writes this key) and why
// the tenant-default seam below can safely treat this key as "user has chosen."
export const LOCALE_STORAGE_KEY = 'dc.locale';

// One namespace per feature/route area so a catalog maps to a screen (ADR-066
// decision 2). `common` holds cross-screen copy (shared column headers, the
// pagination line, Back); `nav` is the sidebar + top-bar label vocabulary shared
// by both the tenant and admin shells. The externalization sweep (b) grows this
// list one namespace per remaining screen.
export const NAMESPACES = [
  'common',
  'login',
  'nav',
  'userMenu',
  'theme',
  'devices',
  'alarms',
  'dashboards',
  'tenants',
  'tiers',
  'connectors',
  'branding',
  'browse',
  'facets',
  'audit',
  'identities',
  'roles',
] as const;

// Catalogs are bundled statically: the corpus is still small enough that a
// build-time import is simplest and has no loading state (hence useSuspense:false
// below). The full corpus (sub-workstream b) switches to lazy per-namespace
// loading so a screen pays only for its own catalog — NAMESPACES is the seam.
const resources = {
  en: {
    common: enCommon,
    login: enLogin,
    nav: enNav,
    userMenu: enUserMenu,
    theme: enTheme,
    devices: enDevices,
    alarms: enAlarms,
    dashboards: enDashboards,
    tenants: enTenants,
    tiers: enTiers,
    connectors: enConnectors,
    branding: enBranding,
    browse: enBrowse,
    facets: enFacets,
    audit: enAudit,
    identities: enIdentities,
    roles: enRoles,
  },
  es: {
    common: esCommon,
    login: esLogin,
    nav: esNav,
    userMenu: esUserMenu,
    theme: esTheme,
    devices: esDevices,
    alarms: esAlarms,
    dashboards: esDashboards,
    tenants: esTenants,
    tiers: esTiers,
    connectors: esConnectors,
    branding: esBranding,
    browse: esBrowse,
    facets: esFacets,
    audit: esAudit,
    identities: esIdentities,
    roles: esRoles,
  },
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
    // Keys are flat semantic identifiers, never a dotted tree — so `.` is a
    // literal key character, not a path separator. This MUST match
    // i18next-parser.config.js (keySeparator:false) or the sweep (b) would write
    // a dotted key the runtime then can't resolve (rendering the raw key). `:`
    // stays the namespace separator (t('common:back')), which is the i18next
    // default but pinned here for the same don't-drift reason.
    keySeparator: false,
    nsSeparator: ':',
    interpolation: {
      // React escapes rendered values already; i18next must not double-escape.
      escapeValue: false,
    },
    detection: {
      // ADR-066 locale precedence, minus the tenant default: (1) an explicit
      // user choice in localStorage, then (3) the browser's languages, then (4)
      // the `en` fallbackLng. Rung (2) — the tenant-default locale on the ADR-038
      // cascade — is NOT in this array on purpose: it must lose to an explicit
      // user choice and beat the browser, which `applyTenantDefaultLocale` below
      // expresses directly. `caches: []` keeps the detector from writing
      // LOCALE_STORAGE_KEY, so that key stays a pure "user chose" signal.
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: LOCALE_STORAGE_KEY,
      caches: [],
    },
    react: {
      // Resources are bundled (no async load), so a Suspense boundary would only
      // add a needless fallback flash. When (b) moves to lazy catalogs, turn this
      // on and wrap the app in <Suspense>.
      useSuspense: false,
    },
  });

/**
 * Persist an explicit user locale choice and switch to it. The switcher calls
 * this; it is the ONLY writer of LOCALE_STORAGE_KEY, which is what lets
 * `applyTenantDefaultLocale` treat that key as an unambiguous "user has chosen"
 * signal. Silently ignores a locale the console does not ship.
 */
export function setUserLocale(code: string): void {
  if (!SUPPORTED_LOCALES.some((l) => l.code === code)) return;
  localStorage.setItem(LOCALE_STORAGE_KEY, code);
  void i18n.changeLanguage(code);
}

/**
 * The seam for ADR-066 sub-workstream (d): a tenant-default locale delivered on
 * the ADR-038 white-label cascade. The tenant provider will call this once it
 * knows the tenant's default locale; wiring that call is (d), but the precedence
 * contract lives here so (a) fixes it and a later slice cannot get it subtly
 * wrong. It is a NO-OP when the user has already made an explicit choice (rung 1
 * beats rung 2) and ignores a locale we do not ship — so a tenant default only
 * ever fills in for a user who has not chosen, and only with a shipped locale.
 */
export function applyTenantDefaultLocale(locale: string | null | undefined): void {
  if (!locale) return;
  // An explicit user choice wins — but only a choice that still resolves to a
  // shipped locale. A stale/unshipped stored value (a dropped locale, a legacy
  // or hand-edited key) has no runtime effect (detection ignores it), so it must
  // NOT block an effective tenant default; treating any string as "chosen" would
  // let an ineffective rung-1 suppress an effective rung-2.
  const chosen = localStorage.getItem(LOCALE_STORAGE_KEY);
  if (chosen && SUPPORTED_LOCALES.some((l) => l.code === chosen)) return;
  if (!SUPPORTED_LOCALES.some((l) => l.code === locale)) return; // never select an unshipped catalog
  void i18n.changeLanguage(locale);
}

export default i18n;
