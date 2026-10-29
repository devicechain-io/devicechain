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
import type { BackendModule } from 'i18next';
import LanguageDetector from 'i18next-browser-languagedetector';

import { LOADERS } from './catalogLoaders';

import enCommon from './locales/en/common.json';
import enLogin from './locales/en/login.json';
import enNav from './locales/en/nav.json';
import enUserMenu from './locales/en/userMenu.json';
import enTheme from './locales/en/theme.json';
import enDevices from './locales/en/devices.json';
import enAlarms from './locales/en/alarms.json';
import enTenants from './locales/en/tenants.json';
import enTiers from './locales/en/tiers.json';
import enConnectors from './locales/en/connectors.json';
import enProvisioning from './locales/en/provisioning.json';
import enUpdateSigners from './locales/en/updateSigners.json';
import enBranding from './locales/en/branding.json';
import enBasemap from './locales/en/basemap.json';
import enLocale from './locales/en/locale.json';
import enBrowse from './locales/en/browse.json';
import enFacets from './locales/en/facets.json';
import enAudit from './locales/en/audit.json';
import enIdentities from './locales/en/identities.json';
import enRoles from './locales/en/roles.json';
import enMembers from './locales/en/members.json';
import enSecurity from './locales/en/security.json';
import enAiPackaging from './locales/en/aiPackaging.json';
import enAdminSettings from './locales/en/adminSettings.json';
import enAdminAudit from './locales/en/adminAudit.json';
import enEntities from './locales/en/entities.json';
import enVersions from './locales/en/versions.json';


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
  // The admin console's chrome. Lazy English (see lazyEnglish.ts): imported by routes/admin/catalog.ts,
  // not bundled here, so the tenant console's first download does not carry it.
  'adminNav',
  'userMenu',
  'theme',
  'devices',
  'alarms',
  'dashboards',
  'deviceProfiles',
  'tenants',
  'tiers',
  'connectors',
  'provisioning',
  'trustAnchors',
  'integrations',
  'commandBatches',
  'updateSigners',
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
  // Admin-only English, lazy like adminNav: imported by routes/admin/ai-providers/catalog.ts.
  'aiProviders',
  'identityProviders',
  'aiPackaging',
  'adminSettings',
  'adminAudit',
  // The registry entity families (device/asset/customer/area types, instances,
  // groups). One shared namespace, keyed by each family's `i18nKey` prefix — the
  // generic list/detail/form pages resolve `${i18nKey}TitlePlural` etc. against it.
  'entities',
  'versions',
  'updates',
  // The tenant's own team: members, invitations, the tenant's roles, and the customer
  // users seen from a customer's page.
  'members',
  // The signed-in person's own two-factor authentication, and the tenant's requirement of it.
  'security',
] as const;

// English — the fallback — is bundled and synchronous, so the first paint in English needs no
// extra request and `t()` works the instant this module has run. Every OTHER locale is loaded on
// demand (see catalogLoaders.ts): a Spanish operator pays for the Spanish catalogs, an English
// one never does.
// The namespaces NOT listed here have their English catalog registered by the first module that uses
// them (i18n/lazy/<ns>.ts, see lazyEnglish.ts), so they ship with their route instead of in this chunk.
// Only the large catalogs are lazy: a small one saves little of the first download and costs a chunk of
// its own (separate gzip streams lose the shared dictionary).
const enResources = {
  en: {
    common: enCommon,
    login: enLogin,
    nav: enNav,
    userMenu: enUserMenu,
    theme: enTheme,
    devices: enDevices,
    alarms: enAlarms,
    tenants: enTenants,
    tiers: enTiers,
    connectors: enConnectors,
    provisioning: enProvisioning,
    updateSigners: enUpdateSigners,
    branding: enBranding,
    basemap: enBasemap,
    locale: enLocale,
    browse: enBrowse,
    facets: enFacets,
    audit: enAudit,
    identities: enIdentities,
    roles: enRoles,
    aiPackaging: enAiPackaging,
    adminSettings: enAdminSettings,
    adminAudit: enAdminAudit,
    entities: enEntities,
    versions: enVersions,
    members: enMembers,
    security: enSecurity,
  },
} as const;

/**
 * A minimal i18next backend over LOADERS (catalogLoaders.ts). i18next asks for one
 * (language, namespace) at a time; the locale's chunk is imported once (the module cache makes the later calls free) and the
 * namespace is read out of it. A language with no loader answers an error, never an empty bundle,
 * so a missing catalog is loud rather than a silent render of raw keys.
 */
const lazyCatalogs: BackendModule = {
  type: 'backend',
  init() {},
  read(language, namespace, callback) {
    const load = LOADERS[language];
    if (!load) {
      callback(new Error(`no catalog for locale "${language}"`), null);
      return;
    }
    load().then(
      (m) => {
        // `m` is undefined when chunkReload claimed a failed chunk (see catalogLoaders.ts).
        // Every path must reach the callback: a throw here would leave i18next waiting on this
        // namespace forever, and i18nReady — and the console's first render — with it.
        const catalog = m?.default[namespace];
        if (catalog) callback(null, catalog);
        else callback(new Error(`no "${namespace}" catalog for locale "${language}"`), null);
      },
      (err: unknown) => callback(err instanceof Error ? err : new Error(String(err)), null),
    );
  },
};

/**
 * Resolves once the catalogs for the language in effect are loaded (immediately for English).
 * main.tsx holds the first render on it, so a Spanish operator never sees English flash first;
 * a failed load (offline, a purged chunk) resolves anyway and the app renders in the English
 * fallback rather than not at all.
 */
export const i18nReady = i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .use(lazyCatalogs)
  .init({
    resources: enResources,
    // Only English is in the bundle; every other locale's namespaces come through the
    // backend below. Without this, giving `resources` at all would mean "nothing to load".
    partialBundledLanguages: true,
    // es-MX loads the es catalog, not a (non-existent) es-MX one.
    load: 'languageOnly',
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
      // Left off on purpose: a Suspense boundary per component would flash a fallback on every
      // namespace read. The one load that matters, the language in effect at startup, is awaited
      // once at the root via i18nReady; a runtime switch awaits changeLanguage, which only
      // switches once the new catalogs are in.
      useSuspense: false,
    },
  })
  .catch(() => undefined);

/**
 * Keep `<html lang>` in step with the language actually in effect.
 *
 * 🔴 NOT DECORATION. The document language is what a screen reader picks its voice
 * and pronunciation rules from, and what a browser offers "translate this page"
 * against; a Spanish page still declaring `lang="en"` is read aloud in an English
 * voice, word by word, for the whole session. index.html ships `lang="en"` as the
 * static default, so without this the attribute is a lie for every user whose locale
 * resolved to anything else — and nothing in the app or the build would notice.
 *
 * 🔴 TWO OBLIGATIONS, NOT ONE, and they are separate on purpose. The listener covers
 * every LATER change (the switcher, the tenant default, the revert on leaving a tenant).
 * The direct call covers the language i18next resolved INSIDE `init()` — when the
 * catalogs are already in hand (English, or a locale a test preloaded) that resolution is
 * synchronous and its `languageChanged` fires BEFORE this listener exists, so a user who lands on Spanish from their browser
 * and never touches the switcher is served entirely by the direct call. Deleting either
 * one leaves the other looking sufficient; documentLanguage.test.ts exists because a
 * test that only ever asserts after a `changeLanguage` cannot tell them apart.
 */
function syncDocumentLanguage(): void {
  // `document` is absent in a non-DOM context (an SSR/node import of this module for
  // its catalogs). The language sync is the only thing that has nothing to do there.
  if (typeof document === 'undefined') return;
  // The RESOLVED language, never the raw request: `es-MX` renders the `es` catalog
  // (nonExplicitSupportedLngs), so announcing `es-MX` would name a catalog that does
  // not exist.
  document.documentElement.lang = i18n.resolvedLanguage ?? DEFAULT_LOCALE;
}
i18n.on('languageChanged', syncDocumentLanguage);
syncDocumentLanguage();

/**
 * Run a language change whose catalogs may still be loading. changeLanguage only switches once
 * they are in. A failed load (offline, a purged chunk) does not reject it: i18next completes the
 * switch with nothing to show, `resolvedLanguage` stays on the English fallback (what is
 * actually rendered, and what `<html lang>` and the switcher read), and the failed namespaces are
 * not retried until the page reloads. The catch is for anything else, so a fire-and-forget
 * caller never leaves an unhandled rejection behind.
 */
function switchLanguage(change: Promise<unknown>): void {
  change.catch(() => undefined);
}

/**
 * Persist an explicit user locale choice and switch to it. The switcher calls
 * this; it is the ONLY writer of LOCALE_STORAGE_KEY, which is what lets
 * `applyTenantDefaultLocale` treat that key as an unambiguous "user has chosen"
 * signal. Silently ignores a locale the console does not ship.
 */
export function setUserLocale(code: string): void {
  if (!SUPPORTED_LOCALES.some((l) => l.code === code)) return;
  localStorage.setItem(LOCALE_STORAGE_KEY, code);
  switchLanguage(i18n.changeLanguage(code));
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
  switchLanguage(i18n.changeLanguage(locale));
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
 * changes. Measured, not assumed — `es-MX`, `es-mx`, `es-419` and `es-Latn-MX` are
 * supported and render Spanish; `ES`, `pt-BR`, `fr-CA` and `zh-Hans-CN` are not and render
 * English.
 *
 * 🔴 ONE CLASS OF TAG WHERE THE TWO STILL DISAGREE, measured the same way and recorded
 * because "asks i18next" reads like it cannot: `ES-MX`, `eS-mx` and `ES-419` are answered
 * NO here and rendered as Spanish by changeLanguage. It is i18next's own seam —
 * isSupportedCode splits the tag before formatting it, while the resolve hierarchy
 * canonicalises the whole tag first — not a fold of ours. The disagreement is FAIL-CLOSED
 * (a tag that would have worked is refused, never a tag that would render raw keys), no
 * browser sends an upper-cased language subtag, and the `locale.default` editor refuses the
 * same tags through this same function, so the tenant cannot end up storing one. Left as is
 * rather than papered over with a second normalisation, which is the thing this function
 * exists to not have.
 *
 * The full tag is still what gets passed to changeLanguage, never a folded base: i18next
 * does the folding, so the day an `es-MX` catalog ships it is picked up with no change
 * here.
 *
 * Exported because the operator's `locale.default` editor asks the same question about
 * the same tag, and asked it with its own second copy of the old hand-rolled fold.
 */
export function isShippedLocale(tag: string): boolean {
  // services.languageUtils exists once init() has run, and the services are
  // built synchronously inside it, before any catalog loads. Guarded anyway, and fail-CLOSED: an
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
  switchLanguage(i18n.changeLanguage());
}

export default i18n;
