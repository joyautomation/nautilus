import { readFileSync } from 'node:fs';
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { RUNTIME_LISTED } from './src/lib/runtime-listed.mjs';

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
      // One line under every page: who builds nautilus, and where support is.
      components: { Footer: './src/components/Footer.astro' },
      // Google Analytics — the same GA4 property joyautomation.com reports to
      // (src/app.html there); the docs show up under their own hostname.
      // Starlight pages are full loads, so `config` on each page is enough.
      head: [
        {
          tag: 'script',
          attrs: { src: 'https://www.googletagmanager.com/gtag/js?id=G-5EGNNTRBYC', async: true },
        },
        {
          tag: 'script',
          content:
            'window.dataLayer = window.dataLayer || [];' +
            'function gtag(){dataLayer.push(arguments);}' +
            "gtag('js', new Date());" +
            "gtag('config', 'G-5EGNNTRBYC');",
        },
      ],
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
      // Practical first: the Learn course, then the tasks you do with a
      // controller grouped by what you are doing, then reference. Every page
      // is listed once, by slug, so a new page must be added here:
      // `node scripts/check-links.mjs --sidebar` fails on a page that is
      // missing from it. Page URLs do not depend on the sidebar.
      sidebar: [
        {
          label: 'Learn',
          items: [
            { label: 'Overview', slug: 'learn' },
            { label: 'Getting started', autogenerate: { directory: 'learn/getting-started' } },
            { label: 'Programming', autogenerate: { directory: 'learn/programming' } },
            { label: 'I/O and protocols', autogenerate: { directory: 'learn/io-and-protocols' } },
            { label: 'Operator screens', autogenerate: { directory: 'learn/operator-screens' } },
            { label: 'Deploy and operate', autogenerate: { directory: 'learn/deploy-and-operate' } },
            { label: 'Coming from Logix', autogenerate: { directory: 'learn/logix' } },
            { label: 'Working with an AI agent', autogenerate: { directory: 'learn/ai-agent' } },
            { label: 'Tutorial: your first controller', slug: 'tutorial' },
            { label: 'Getting started (the short version)', slug: 'getting-started' },
            { label: 'Playground (in your browser)', link: '/playground/' },
          ],
        },
        {
          label: 'Programming',
          items: [
            { slug: 'guides/first-program' },
            { slug: 'languages' },
            { slug: 'languages/structured-text' },
            { slug: 'languages/ladder' },
            { slug: 'languages/function-block' },
            { slug: 'languages/sfc' },
            { slug: 'languages/not-supported' },
            { slug: 'guides/blocks-and-tasks' },
            { slug: 'guides/tag-model' },
            { slug: 'guides/forcing' },
            { slug: 'guides/online-edits' },
            { slug: 'guides/program-history' },
          ],
        },
        {
          label: 'I/O and protocols',
          items: [
            { slug: 'guides/modbus' },
            { slug: 'guides/opcua-server' },
            { slug: 'guides/opcua-client' },
            { slug: 'guides/ethernet-ip' },
            { slug: 'guides/sparkplug' },
            { slug: 'guides/sparkplug-host' },
            { slug: 'guides/profinet' },
            { slug: 'guides/io-link' },
            { slug: 'guides/it-hardware' },
            { slug: 'guides/substitutes' },
          ],
        },
        {
          label: 'Operator screens and alarms',
          items: [
            { slug: 'guides/operator-screens' },
            { slug: 'guides/streaming' },
            { slug: 'guides/alarms' },
            { slug: 'guides/historian' },
          ],
        },
        {
          label: 'Deploy and operate',
          items: [
            { slug: 'guides/deployment' },
            { slug: 'guides/auth' },
            { slug: 'guides/audit-trail' },
            { slug: 'guides/real-time' },
            { slug: 'guides/redundancy' },
          ],
        },
        {
          label: 'Working with an AI agent',
          items: [{ slug: 'guides/ai-assistants' }],
        },
        {
          label: 'Coming from…',
          items: [
            { slug: 'coming-from/scada' },
            { slug: 'coming-from/studio-5000' },
            { slug: 'coming-from/tia-portal' },
            { slug: 'coming-from/codesys' },
            { slug: 'guides/logix' },
            { slug: 'guides/logix-authoring' },
          ],
        },
        {
          label: 'Hardware targets',
          items: [
            { slug: 'guides/plcnext' },
            { slug: 'guides/groov-epic' },
            { slug: 'guides/verdin' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { slug: 'guides/http-api' },
            { slug: 'reference/functions' },
            { slug: 'reference/testing' },
            { slug: 'reference/hmi' },
            { slug: 'reference/structure-warnings' },
          ],
        },
        {
          // src/pages/verified/ — built from the latest green rig and CI runs
          // by scripts/fetch-verified.mjs (see README).
          label: 'Verified',
          collapsed: true,
          items: [
            { label: 'Overview', link: '/verified/' },
            { label: 'FBD', link: '/verified/fbd/' },
            { label: 'Ladder', link: '/verified/ladder/' },
            { label: 'SFC', link: '/verified/sfc/' },
            { label: 'Mimic', link: '/verified/mimic/' },
            { label: 'Component', link: '/verified/component/' },
            { label: 'Commands', link: '/verified/commands/' },
            { label: 'Other claims', link: '/verified/other/' },
            ...(RUNTIME_LISTED ? [{ label: 'Runtime', link: '/verified/runtime/' }] : []),
          ],
        },
      ],
    }),
  ],
});
