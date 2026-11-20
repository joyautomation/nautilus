import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';
import { docsLoader } from '@astrojs/starlight/loaders';
import { docsSchema } from '@astrojs/starlight/schema';

export const collections = {
  docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
  // The interactive lessons' text (lessons/<id>/lesson.md); the projects
  // beside it are bundled by scripts/sync-lessons.mjs.
  lessons: defineCollection({
    loader: glob({ pattern: '*/lesson.md', base: './lessons', generateId: ({ entry }) => entry.split('/')[0] }),
    schema: z.object({ title: z.string(), track: z.string().optional(), next: z.string().optional(), carry: z.array(z.string()).optional(), open: z.array(z.string()).optional() }),
  }),
};
