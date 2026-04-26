import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import sitemap from '@astrojs/sitemap';

export default defineConfig({
  site: 'https://tjorri.github.io',
  base: '/paddock',
  trailingSlash: 'ignore',
  build: { format: 'directory' },
  integrations: [
    starlight({
      title: 'Paddock',
      logo: {
        light: './public/brand/paddock-lockup-light.svg',
        dark:  './public/brand/paddock-lockup-dark.svg',
        replacesTitle: true,
      },
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/tjorri/paddock' },
      ],
      customCss: [
        '@fontsource/ibm-plex-sans/400.css',
        '@fontsource/ibm-plex-sans/600.css',
        '@fontsource/ibm-plex-mono/400.css',
        '@fontsource/ibm-plex-mono/600.css',
        './src/styles/tokens.css',
        './src/styles/global.css',
      ],
      sidebar: [
        {
          label: 'Get started',
          items: [
            { label: 'Welcome',    slug: 'docs' },
            { label: 'Install',    slug: 'docs/install' },
            { label: 'Quickstart', slug: 'docs/quickstart' },
          ],
        },
        {
          label: 'Concepts',
          items: [
            { label: 'Overview', link: '/docs/concepts' },
            {
              label: 'Run platform',
              items: [
                { label: 'Harness',   slug: 'docs/concepts/harness' },
                { label: 'Template',  slug: 'docs/concepts/template' },
                { label: 'Run',       slug: 'docs/concepts/run' },
                { label: 'Workspace', slug: 'docs/concepts/workspace' },
              ],
            },
            {
              label: 'Security boundary',
              items: [
                { label: 'Broker',  slug: 'docs/concepts/broker' },
                { label: 'Proxy',   slug: 'docs/concepts/proxy' },
                { label: 'Session', slug: 'docs/concepts/session' },
                { label: 'Bridge',  slug: 'docs/concepts/bridge' },
              ],
            },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Cookbooks',     slug: 'docs/cookbooks' },
            { label: 'CRD reference', slug: 'docs/reference' },
            { label: 'Migrations',    slug: 'docs/migrations' },
          ],
        },
      ],
    }),
    sitemap(),
  ],
});
