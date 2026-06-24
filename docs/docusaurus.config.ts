import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import { themes as prismThemes } from 'prism-react-renderer';

const config: Config = {
  title: 'DeviceChain',
  tagline: 'A modern, cloud-native IoT Application Enablement Platform',
  favicon: 'img/favicon.ico',

  url: 'https://docs.devicechain.io',
  baseUrl: '/',

  organizationName: 'devicechain-io',
  projectName: 'devicechain',

  onBrokenLinks: 'warn',
  onBrokenMarkdownLinks: 'warn',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/', // docs are the site root
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/devicechain-io/devicechain/tree/master/docs/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    navbar: {
      title: 'DeviceChain',
      items: [
        { type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs' },
        {
          href: 'https://github.com/devicechain-io/devicechain',
          label: 'GitHub',
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
