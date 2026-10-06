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

export interface LanguageItem {
  language: string;
  label: string;
  description: string;
}

/** The program languages `naut new --language` accepts (cmd/naut/new.go).
 * Structured Text first: it is the default. */
export const NEW_PROJECT_LANGUAGES: LanguageItem[] = [
  { language: "st", label: "Structured Text", description: "program.st" },
  { language: "ld", label: "Ladder Diagram", description: "program.ld" },
  { language: "fbd", label: "Function Block Diagram", description: "program.fbd" },
  { language: "sfc", label: "Sequential Function Chart", description: "program.sfc" },
];

/** `--language` picks the language of the BLANK program, so it only applies
 * to Minimal and SDK; Demo is the fixed multi-language tour and the SDK demo's
 * plant is ST. The quick pick is skipped for the others. */
export function templateTakesLanguage(template: string): boolean {
  return template === "minimal" || template === "sdk";
}

/** The argv `naut new` gets. `language` is omitted (the CLI defaults to st)
 * for templates that ignore it. */
export function newProjectArgs(name: string, template: string, language?: string): string[] {
  const args = ["new", name, "--no-input", "--template", template];
  if (language && templateTakesLanguage(template)) args.push("--language", language);
  return args;
}
