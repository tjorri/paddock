import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://tjorri.github.io',
  base: '/paddock',
  trailingSlash: 'ignore',
  build: {
    format: 'directory',
  },
});
