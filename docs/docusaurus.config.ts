import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { themes as prismThemes } from 'prism-react-renderer';

// Every markdown tree a docs page is built from, for the search index's content hash.
// A generated GraphQL reference (a second docs plugin instance, written into
// generated/graphql-reference at prebuild) is added to the list when that folder exists,
// because the plugin warns on a `docsDir` that is missing. Resolved against this file, not
// the working directory, so it does not depend on where the build is started.
const generatedReference = resolve(__dirname, 'generated/graphql-reference');
const searchDocsDirs = [
  'docs',
  // The frozen snapshots (`docusaurus docs:version`) are indexed pages too, so they must be in
  // the content hash: a snapshot that changed would otherwise leave the index file name alone.
  'versioned_docs',
  'i18n',
  ...(existsSync(generatedReference) ? ['generated/graphql-reference'] : []),
];

// Released snapshots, newest first, as `docusaurus docs:version` records them. The docs
// plugin's `lastVersion` and `versions` are DERIVED from this file, so cutting a snapshot
// needs no config edit and the site root cannot keep serving an older release than
// versions.json lists. Absent or empty means no snapshot yet: the living docs are served.
const versionsFile = resolve(__dirname, 'versions.json');
const releasedVersions: string[] = existsSync(versionsFile)
  ? JSON.parse(readFileSync(versionsFile, 'utf8'))
  : [];
const docsVersions = {
  current: { label: 'Next', path: 'next', banner: 'unreleased' as const, badge: true },
  // Newest snapshot: no banner. Older ones keep Docusaurus's default 'unmaintained' banner.
  ...Object.fromEntries(
    releasedVersions.map((v, i) => [
      v,
      { label: v, badge: true, ...(i === 0 ? { banner: 'none' as const } : {}) },
    ]),
  ),
};

const config: Config = {
  title: 'DeviceChain',
  tagline: 'A modern, cloud-native IoT Application Enablement Platform',
  favicon: 'img/favicon.svg',

  url: 'https://docs.devicechain.io',
  baseUrl: '/',

  // PostHog analytics: a pinned client served from this site, then its init. Both are
  // plain files under static/ — see static/analytics.js for why it is not the
  // posthog-docusaurus plugin, and static/_redirects for the /ingest proxy.
  scripts: [
    { src: '/posthog/posthog-1.436.1.js', defer: true },
    { src: '/analytics-scrub.js', defer: true },
    { src: '/analytics.js', defer: true },
  ],

  organizationName: 'devicechain-io',
  projectName: 'devicechain',

  // 🔴 `throw`, which is Docusaurus's own default, and the only setting that
  // makes the build a GATE rather than a report.
  //
  // These were 'warn'. The docs are built on every pull request (Netlify's
  // deploy-preview check), so it looked like broken links were covered — but a
  // warning is printed into a build log nobody reads and the deploy goes green.
  // The build ran; it just could not fail.
  //
  // This matters most for the translated tree: docs/i18n/es mirrors every
  // English page, so a relative link fixed in one locale and typo'd in the
  // other is the likeliest way to break a link here, and the least likely to be
  // noticed by eye. Verified zero broken links across both locales when this was
  // flipped, so it starts clean.
  onBrokenLinks: 'throw',
  // Anchors were left at the default ('warn') when the above was flipped, which meant a link
  // to a real page with a WRONG FRAGMENT still shipped: verified by planting one — Docusaurus
  // printed "found broken anchors", then printed [SUCCESS] and exited 0. Netlify would have
  // deployed it. The reader's experience of that is the same as a broken link (they land on
  // the page and the section they were sent to is not there), so it gets the same treatment.
  //
  // The cross-locale case is the sharp edge, and it is not hypothetical: an English heading
  // and its Spanish translation generate DIFFERENT slugs ("Configuration" → #configuration,
  // "Configuración" → #configuración), so a fragment that is correct in one locale is broken
  // in the other by default. The fix is an explicit `{#anchor}` on both headings — pin the
  // anchor whenever you link to a section across locales.
  //
  // Verified zero broken anchors across both locales when this was flipped, so it starts clean.
  onBrokenAnchors: 'throw',

  markdown: {
    hooks: {
      // Moved from the top-level `onBrokenMarkdownLinks`, which is deprecated
      // and removed in Docusaurus v4.
      onBrokenMarkdownLinks: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    // English is the reference; translated locales retain their own routes.
    locales: ['en', 'es', 'zh-CN'],
    localeConfigs: {
      en: { label: 'English' },
      es: { label: 'Español' },
      'zh-CN': { label: '简体中文', htmlLang: 'zh-CN' },
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/', // docs are the site root
          sidebarPath: './sidebars.ts',
          // VERSIONING. `docs/docs` + `i18n/<locale>/.../current` are the LIVING docs (the next,
          // unreleased release); `versioned_docs/version-X.Y` + `versioned_sidebars` +
          // `i18n/<locale>/.../version-X.Y` are FROZEN snapshots cut by
          // `npx docusaurus docs:version X.Y` at each release (see docs/README.md).
          // Readers land on `lastVersion` at the site root (/…); the living docs are served
          // under /next/… with an unreleased banner. Authors keep editing `docs/docs` only.
          // The guards deliberately treat the two differently: translation freshness, the
          // docs-drafts rot guard and the generated-reference checks follow the living tree;
          // ADR-reference scanning and locale parity cover the snapshots too.
          lastVersion: releasedVersions[0] ?? 'current',
          versions: docsVersions,
          editUrl: 'https://github.com/devicechain-io/devicechain/tree/main/docs/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themes: [
    // Local full-text search: the index is built at `docusaurus build` from the rendered
    // HTML of every locale and served as static files, so there is no third-party service.
    [
      '@easyops-cn/docusaurus-search-local',
      {
        // Content-hashed index file names (search-index-<hash>.json).
        //
        // The hash covers the .md/.mdx files under `docsDir` ONLY, nothing else (not
        // sidebars, config or other generated content), so `docsDir` must list every
        // markdown tree the index is built from: the default ('docs') alone would leave the
        // hash unchanged when only a translation changed.
        hashed: 'filename',
        docsDir: searchDocsDirs,
        // `language` is one list for the whole build, not one per locale, and the build
        // produces all three locales at once. `zh` is the tokenizer Chinese needs (no
        // spaces between words); `es` is the Spanish stemmer.
        language: ['en', 'es', 'zh'],
        // Docs are served at the site root. The plugin collects the pages of EVERY docs
        // plugin instance by permalink, so a second instance (such as a generated
        // GraphQL reference) is picked up under this same root when it is added, with no
        // further configuration. There is no blog.
        docsRouteBasePath: '/',
        indexBlog: false,
        // Off: highlighting appends the typed words to a result's URL (?_highlight=), which
        // would put raw queries into shared links and analytics URLs.
        highlightSearchTermsOnTargetPage: false,
        explicitSearchResultPath: true,
      },
    ],
  ],

  plugins: [
    // The per-schema GraphQL reference, served at /reference/graphql/. Its pages are
    // GENERATED by scripts/generate-graphql-reference.mjs (the prebuild/prestart step,
    // beside the schema publisher) and never committed, so a build that skipped the
    // generator fails here on a missing folder rather than shipping without them. A
    // separate instance keeps generated pages out of docs/ and its translations, which
    // the locale-parity and translation-freshness guards check as hand-written pages.
    // The id is the generator's PLUGIN_ID: it names the translated folders too.
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'graphql-reference',
        path: 'generated/graphql-reference',
        routeBasePath: 'reference/graphql',
      },
    ],
  ],

  themeConfig: {
    // Dark by default, matching the console and the marketing site — the brand
    // is a navy canvas and the light theme is the exception, not the baseline.
    // respectPrefersColorScheme still lets a reader who has asked their OS for
    // light get light, so this sets the default rather than forcing a theme.
    colorMode: {
      defaultMode: 'dark',
      respectPrefersColorScheme: true,
    },
    // The card a link to any docs page shows when it is shared (og:image and
    // twitter:image). It is the marketing site's card, kept identical so a
    // shared link looks the same whichever site it points at.
    image: 'img/social-card.png',
    navbar: {
      title: 'DeviceChain',
      logo: {
        alt: 'DeviceChain',
        src: 'img/logo.svg',
      },
      items: [
        { type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs' },
        // Version switcher: the released snapshots plus Next (the living docs).
        { type: 'docsVersionDropdown', position: 'right' },
        { type: 'localeDropdown', position: 'right' },
        // Rendered as icons, not text — see `.navbar-social` in src/css/custom.css
        // for how, and for why the label below still has to be a real word rather
        // than an empty string.
        {
          href: 'https://github.com/devicechain-io/devicechain',
          label: 'GitHub',
          'aria-label': 'DeviceChain on GitHub',
          className: 'navbar-social navbar-social--github',
          position: 'right',
        },
        {
          href: 'https://x.com/DeviceChain_IoT',
          label: 'X',
          'aria-label': 'DeviceChain on X',
          className: 'navbar-social navbar-social--x',
          position: 'right',
        },
        {
          href: 'https://www.youtube.com/@devicechain-iot',
          label: 'YouTube',
          'aria-label': 'DeviceChain on YouTube',
          className: 'navbar-social navbar-social--youtube',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            { label: 'Introduction', to: '/' },
            { label: 'Concepts', to: '/concepts/architecture' },
            { label: 'Guides', to: '/category/guides' },
          ],
        },
        {
          title: 'Project',
          items: [
            { label: 'GitHub', href: 'https://github.com/devicechain-io/devicechain' },
            {
              label: 'Discussions',
              href: 'https://github.com/devicechain-io/devicechain/discussions',
            },
            { label: 'Getting help', to: '/getting-help' },
            // The navbar icon is unlabelled by design; this is the one place the
            // handle itself is written out, so someone can find the account by
            // name rather than only by clicking a glyph.
            { label: 'Follow @DeviceChain_IoT', href: 'https://x.com/DeviceChain_IoT' },
            { label: 'YouTube', href: 'https://www.youtube.com/@devicechain-iot' },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} DeviceChain. Apache 2.0.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['go', 'graphql', 'hcl', 'bash', 'json'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
