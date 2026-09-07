import { readFileSync } from 'node:fs';
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// The VS Code extension's TextMate grammars, reused as-is so IEC code blocks
// highlight the way they do in the editor. Fence tags: iecst, iecld, iecfbd,
// iecsfc (aliases: st, ld, fbd, sfc).
const grammar = (lang) => ({
  ...JSON.parse(readFileSync(new URL(`../tools/vscode-iec/syntaxes/iec-${lang}.tmLanguage.json`, import.meta.url), 'utf8')),
  name: `iec${lang}`,
  aliases: [lang],
});

export default defineConfig({
  site: 'https://nautilus.joyautomation.com',
  integrations: [
    starlight({
      title: 'nautilus',
      favicon: '/favicon.png',
      logo: {
        light: './src/assets/logo-light.svg',
        dark: './src/assets/logo.svg',
        alt: 'nautilus',
      },
      customCss: ['./src/styles/fonts.css'],
      expressiveCode: { shiki: { langs: ['st', 'ld', 'fbd', 'sfc'].map(grammar) } },
      description:
        'SCADA, built like software — a Go + SvelteKit toolkit for industrial control and supervisory systems with version control, tests, CI/CD, and code review.',
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/joyautomation/nautilus',
        },
      ],
      editLink: {
        baseUrl: 'https://github.com/joyautomation/nautilus/edit/main/website/',
      },
      sidebar: [
        {
          label: 'Start here',
          items: [{ label: 'Getting started', slug: 'getting-started' }],
        },
        {
          label: 'Languages',
          autogenerate: { directory: 'languages' },
        },
        {
          label: 'Guides',
          autogenerate: { directory: 'guides' },
        },
        {
          label: 'Reference',
          autogenerate: { directory: 'reference' },
        },
      ],
    }),
  ],
});
