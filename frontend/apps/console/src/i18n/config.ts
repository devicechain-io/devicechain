// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// i18next initialization for the console (ADR-066). This is the framework-wiring
// half of the i18n work: the provider, locale detection, and the catalog wiring.
// Importing this module for its side effect (see main.tsx) initializes the shared
// i18next instance; components consume it through react-i18next's useTranslation.
//
// The framework wiring landed as ADR-066 sub-workstream (a) — English only, two
// namespaces, one converted reference screen (Login) — and the string-externalization
// sweep (b), the Spanish catalog (c) and the tenant-default locale (d) followed as
// their own workstreams. All four are in. What is still marked below is the seam each
// one left: NAMESPACES for lazy per-namespace loading, and applyTenantDefaultLocale
// for the tenant rung, which TenantProvider now calls.

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
import enDeviceProfiles from './locales/en/deviceProfiles.json';
import enTenants from './locales/en/tenants.json';
import enTiers from './locales/en/tiers.json';
import enConnectors from './locales/en/connectors.json';
import enCommandBatches from './locales/en/commandBatches.json';
import enBranding from './locales/en/branding.json';
import enBasemap from './locales/en/basemap.json';
import enLocale from './locales/en/locale.json';
import enBrowse from './locales/en/browse.json';
import enFacets from './locales/en/facets.json';
import enAudit from './locales/en/audit.json';
import enIdentities from './locales/en/identities.json';
import enRoles from './locales/en/roles.json';
import enAiProviders from './locales/en/aiProviders.json';
import enAiPackaging from './locales/en/aiPackaging.json';
import enAdminSettings from './locales/en/adminSettings.json';
import enAdminAudit from './locales/en/adminAudit.json';
import enEntities from './locales/en/entities.json';
import enVersions from './locales/en/versions.json';

import esCommon from './locales/es/common.json';
import esLogin from './locales/es/login.json';
import esNav from './locales/es/nav.json';
import esUserMenu from './locales/es/userMenu.json';
import esTheme from './locales/es/theme.json';
import esDevices from './locales/es/devices.json';
import esAlarms from './locales/es/alarms.json';
import esDashboards from './locales/es/dashboards.json';
import esDeviceProfiles from './locales/es/deviceProfiles.json';
import esTenants from './locales/es/tenants.json';
import esTiers from './locales/es/tiers.json';
import esConnectors from './locales/es/connectors.json';
import esCommandBatches from './locales/es/commandBatches.json';
import esBranding from './locales/es/branding.json';
import esBasemap from './locales/es/basemap.json';
import esLocale from './locales/es/locale.json';
import esBrowse from './locales/es/browse.json';
import esFacets from './locales/es/facets.json';
import esAudit from './locales/es/audit.json';
import esIdentities from './locales/es/identities.json';
import esRoles from './locales/es/roles.json';
import esAiProviders from './locales/es/aiProviders.json';
import esAiPackaging from './locales/es/aiPackaging.json';
import esAdminSettings from './locales/es/adminSettings.json';
import esAdminAudit from './locales/es/adminAudit.json';
import esEntities from './locales/es/entities.json';
import esVersions from './locales/es/versions.json';

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
  'deviceProfiles',
  'tenants',
  'tiers',
  'connectors',
  'commandBatches',
  'branding',
  'basemap',
  // The tenant's own DEFAULT-language editor (rung 2 of the precedence below), not
  // the machinery in this file. Named for the screen it backs, like every other
  // namespace here.
  'locale',
  'browse',
  'facets',
  'audit',
  'identities',
  'roles',
  'aiProviders',
  'aiPackaging',
  'adminSettings',
  'adminAudit',
  // The registry entity families (device/asset/customer/area types, instances,
  // groups). One shared namespace, keyed by each family's `i18nKey` prefix — the
  // generic list/detail/form pages resolve `${i18nKey}TitlePlural` etc. against it.
  'entities',
  'versions',
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
    deviceProfiles: enDeviceProfiles,
    tenants: enTenants,
    tiers: enTiers,
    connectors: enConnectors,
    commandBatches: enCommandBatches,
    branding: enBranding,
    basemap: enBasemap,
    locale: enLocale,
    browse: enBrowse,
    facets: enFacets,
    audit: enAudit,
    identities: enIdentities,
    roles: enRoles,
    aiProviders: enAiProviders,
    aiPackaging: enAiPackaging,
    adminSettings: enAdminSettings,
    adminAudit: enAdminAudit,
    entities: enEntities,
    versions: enVersions,
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
    deviceProfiles: esDeviceProfiles,
    tenants: esTenants,
    tiers: esTiers,
    connectors: esConnectors,
    commandBatches: esCommandBatches,
    branding: esBranding,
    basemap: esBasemap,
    locale: esLocale,
    browse: esBrowse,
    facets: esFacets,
    audit: esAudit,
    identities: esIdentities,
    roles: esRoles,
    aiProviders: esAiProviders,
    aiPackaging: esAiPackaging,
    adminSettings: esAdminSettings,
    adminAudit: esAdminAudit,
    entities: esEntities,
    versions: esVersions,
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
 * Precedence rung 2 (ADR-066 sub-workstream d): the tenant's default locale,
 * delivered on the ADR-038 white-label cascade.
 *
 * TenantProvider calls this once, from a single effect keyed on the tenant's
 * EFFECTIVE locale — it is the only caller, and it must stay the only one. It is a
 * NO-OP when the user has already made an explicit choice (rung 1 beats rung 2) and
 * ignores a locale that resolves to no shipped catalog, so a tenant default only ever
 * fills in for a user who has not chosen, and only with a locale that can render.
 *
 * 🔴 IT BEATS RUNG 3 BY OVERWRITING IT, which is why this is a function rather than
 * an entry in `detection.order` above. Detection has already run and resolved the
 * browser's language by the time a tenant is known, so applying a tenant default
 * means calling changeLanguage on a language that is already in effect. Anything
 * added here that skips when a language is resolved (an `if (i18n.resolvedLanguage)
 * return`, an "only run once" flag) silently demotes rung 2 below rung 3 while
 * leaving every rung-1 test green — see the browser-rung tests in config.test.ts,
 * which exist for that mutation specifically.
 */
export function applyTenantDefaultLocale(locale: string | null | undefined): void {
  if (!locale) return;
  // An explicit user choice wins — but only a choice that still resolves to a
  // shipped locale. A stale/unshipped stored value (a dropped locale, a legacy
  // or hand-edited key) has no runtime effect (detection ignores it), so it must
  // NOT block an effective tenant default; treating any string as "chosen" would
  // let an ineffective rung-1 suppress an effective rung-2.
  const chosen = localStorage.getItem(LOCALE_STORAGE_KEY);
  if (chosen && isShippedLocale(chosen)) return;
  if (!isShippedLocale(locale)) return; // never select a locale that would render raw keys
  void i18n.changeLanguage(locale);
}

/**
 * Whether a language tag resolves to a catalog this build actually ships.
 *
 * 🔴 IT ASKS i18next RATHER THAN RE-DERIVING THE ANSWER, and that is the whole point.
 * The question "would this tag render one of our catalogs?" has exactly one correct
 * answer — the one `changeLanguage` is about to act on — and any second implementation
 * of it is a source of disagreement rather than a convenience. The first version here
 * was hand-rolled (`code === tag || code.toLowerCase() === base`) and disagreed on
 * case: it called `ES` resolvable, handed it to `changeLanguage`, and i18next resolved
 * `en`, because `lowerCaseLng` is off and i18next only case-normalizes the REGION half
 * of a hyphenated tag. The seam was selecting a language that renders raw keys by its
 * own definition.
 *
 * `isSupportedCode` applies `nonExplicitSupportedLngs` itself, so the regional fold
 * this function used to hand-roll comes for free and stays correct if that option ever
 * changes. Measured, not assumed — `es-MX`, `es-419` and `es-Latn-MX` are supported and
 * render Spanish; `ES`, `pt-BR`, `fr-CA` and `zh-Hans-CN` are not and render English.
 *
 * The full tag is still what gets passed to changeLanguage, never a folded base: i18next
 * does the folding, so the day an `es-MX` catalog ships it is picked up with no change
 * here.
 *
 * Exported because the operator's `locale.default` editor asks the same question about
 * the same tag, and asked it with its own second copy of the old hand-rolled fold.
 */
export function isShippedLocale(tag: string): boolean {
  // services.languageUtils exists once init() has run, and init is synchronous here
  // (catalogs are bundled, no async backend). Guarded anyway, and fail-CLOSED: an
  // unanswerable question must not select a language.
  const utils = i18n.services?.languageUtils;
  return utils ? utils.isSupportedCode(tag) : false;
}

/**
 * Hand the language back to the rungs that do not need a tenant: an explicit user
 * choice, then the browser, then English.
 *
 * TenantProvider calls this when it unmounts — leaving the tenant shell for the login
 * screen or the instance-scoped /admin console — so a tenant default does not outlive
 * the tenant it belongs to.
 *
 * 🔴 The no-argument `changeLanguage()` RE-RUNS DETECTION rather than picking a
 * constant, which is what makes this a revert rather than a second opinion: it lands on
 * whatever rungs 1, 3 and 4 say, with no knowledge of them here. Measured on all three
 * arms — a stored user choice wins, otherwise a shipped browser language wins,
 * otherwise English.
 */
export function resetToDetectedLocale(): void {
  void i18n.changeLanguage();
}

export default i18n;
