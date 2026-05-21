import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'QUARK ORM',
  tagline: 'Type-safe, security-first ORM for Go — generics on the surface, six dialects underneath.',
  favicon: 'img/quark-logo.svg',

  // Future flags, see https://docusaurus.io/docs/api/docusaurus-config#future
  future: {
    v4: true, // Improve compatibility with the upcoming Docusaurus v4
  },

  // Set the production url of your site here
  url: 'https://jcsvwinston.github.io',
  // Set the /<baseUrl>/ pathname under which your site is served
  // For GitHub pages deployment, it is often '/<projectName>/'
  baseUrl: '/quark/',

  // GitHub pages deployment config.
  // The site source lives in this repo under website/. The
  // .github/workflows/deploy.yml workflow builds and uploads it via
  // actions/deploy-pages, so the legacy `gh-pages` branch is not used.
  organizationName: 'jcsvwinston',
  projectName: 'quark',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  // Even if you don't use internationalization, you can use this field to set
  // useful metadata like html lang. For example, if your site is Chinese, you
  // may want to replace "en" with "zh-Hans".
  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  themes: [
    '@docusaurus/theme-mermaid',
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        language: ['en'],
        docsRouteBasePath: '/docs',
      },
    ],
  ],

  markdown: {
    mermaid: true,
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl:
            'https://github.com/jcsvwinston/quark/tree/main/website/',
        },
        blog: {
          showReadingTime: true,
          feedOptions: {
            type: ['rss', 'atom'],
            xslt: true,
          },
          editUrl:
            'https://github.com/jcsvwinston/quark/tree/main/website/',
          // Useful options to enforce blogging best practices
          onInlineTags: 'warn',
          onInlineAuthors: 'warn',
          onUntruncatedBlogPosts: 'warn',
        },
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/quark-social-card.svg',
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'QUARK',
      logo: {
        alt: 'QUARK logo',
        src: 'img/quark-logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'quarkSidebar',
          position: 'left',
          label: 'Docs',
        },
        {to: '/docs/guides/getting-started', label: 'Quickstart', position: 'left'},
        {to: '/docs/reference/benchmarks', label: 'Benchmarks', position: 'left'},
        {
          to: '/blog',
          label: 'Blog',
          position: 'left',
        },
        {
          href: 'https://github.com/jcsvwinston/quark',
          label: 'GitHub',
          position: 'right',
        },
        {
          href: 'https://pkg.go.dev/github.com/jcsvwinston/quark',
          label: 'API ↗',
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
            {
              label: 'Getting Started',
              to: '/docs/guides/getting-started',
            },
            {
              label: 'Query Builder',
              to: '/docs/guides/querying',
            },
            {
              label: 'Migrations',
              to: '/docs/guides/migrations',
            },
          ],
        },
        {
          title: 'Reference',
          items: [
            {
              label: 'Architecture',
              to: '/docs/reference/architecture',
            },
            {
              label: 'Dialects',
              to: '/docs/reference/dialects',
            },
            {
              label: 'Roadmap',
              to: '/docs/reference/roadmap',
            },
          ],
        },
        {
          title: 'More',
          items: [
            {
              label: 'Repo',
              href: 'https://github.com/jcsvwinston/quark',
            },
            {
              label: 'Releases',
              href: 'https://github.com/jcsvwinston/quark/releases',
            },
            {
              label: 'Issues',
              href: 'https://github.com/jcsvwinston/quark/issues',
            },
            {
              label: 'pkg.go.dev',
              href: 'https://pkg.go.dev/github.com/jcsvwinston/quark',
            },
            {
              label: 'Apache 2.0 license',
              href: 'https://github.com/jcsvwinston/quark/blob/main/LICENSE',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} QUARK ORM. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['go', 'sql', 'bash'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
