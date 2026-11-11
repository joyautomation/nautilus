// Which files declare a project's tags: the manifest (nautilus.yaml) and
// the tag files beside it (tags/*.yaml, tags/*.yml). A diagram's model
// carries those tags — the ladder's "declare N names…" offer is computed
// from them — so a save to any of these files has to re-post the model of
// every open diagram, not only an edit to the diagram's own text.

/** The watcher glob for the same set of files. */
export const PROJECT_TAGS_GLOB = "**/{nautilus.yaml,tags/*.yaml,tags/*.yml}";

/** True for a path (either separator) the glob above matches. */
export function isProjectTagsPath(p: string): boolean {
  const parts = p.split(/[\\/]/);
  const base = parts[parts.length - 1] ?? "";
  if (base === "nautilus.yaml") return true;
  return parts[parts.length - 2] === "tags" && /\.ya?ml$/.test(base);
}
