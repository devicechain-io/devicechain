import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import { themes as prismThemes } from 'prism-react-renderer';

const config: Config = {
  title: 'DeviceChain',
  tagline: 'A modern, cloud-native IoT Application Enablement Platform',
  favicon: 'img/favicon.svg',

  url: 'https://docs.devicechain.io',
  baseUrl: '/',

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
    // Spanish is the GA docs locale (ADR-066). Additional locales (de, pt-BR, ja)
    // ship post-GA, matching the app's locale roadmap.
    locales: ['en', 'es'],
    localeConfigs: {
      en: { label: 'English' },
      es: { label: 'Español' },
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/', // docs are the site root
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/devicechain-io/devicechain/tree/main/docs/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
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
    navbar: {
      title: 'DeviceChain',
      logo: {
        alt: 'DeviceChain',
        src: 'img/logo.svg',
      },
      items: [
        { type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs' },
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
            { label: 'Guides', to: '/guides/local-development' },
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
