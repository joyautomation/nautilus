// Pure logic for "nautilus: Create Project…" (newProject.ts) — no vscode
// import, so it's plain-Node testable like cliResolve.ts/cliInstall.ts.

/** Mirrors cmd/naut/new.go's own validation (`strings.ContainsAny(s, "
 * /\\")`), so the input box rejects a bad name before spawning the CLI
 * rather than relaying the CLI's stderr back through a second dialog. */
export function validateProjectName(name: string): string | undefined {
  const trimmed = name.trim();
  if (!trimmed) return "a name is required";
  if (/[ /\\]/.test(trimmed)) return "no spaces or slashes";
  return undefined;
}

export interface TemplateItem {
  template: string;
  label: string;
  description: string;
}

/** Same four templates and descriptions `naut new`'s own interactive form
 * offers (cmd/naut/new.go's templateSelect) — Demo first, since it's the
 * default and the fastest way to see everything working. */
export const NEW_PROJECT_TEMPLATES: TemplateItem[] = [
  {
    template: "demo",
    label: "Demo",
    description: "runnable tour: 3 tasks, 3 languages, simulated plant",
  },
  {
    template: "minimal",
    label: "Minimal",
    description: "one task, one program, one test",
  },
  {
    template: "sdk",
    label: "SDK",
    description: "Go project with a driver seam to fill in",
  },
  {
    template: "sdk-demo",
    label: "SDK demo",
    description: "Go project with plant physics in Go",
  },
];

/** The argv `naut new` gets. */
export function newProjectArgs(name: string, template: string): string[] {
  return ["new", name, "--no-input", "--template", template];
}
