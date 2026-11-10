// Pure logic for AGENTS.md (agentsMd.ts): what `naut agents -check -json`
// said, and what to offer about it. No vscode import, so it is plain-Node
// testable. The guidance text itself is never here: it comes from the naut
// binary (cmd/naut/agents.go), so it always matches the checks that naut
// runs (#645).

export type AgentsMdState = "missing" | "current" | "stale" | "unmanaged";
export type ClaudeMdState = "missing" | "current" | "noimport";

export interface AgentsCheck {
  agentsMd: AgentsMdState;
  claudeMd: ClaudeMdState;
  version: string;
}

const AGENTS: readonly string[] = ["missing", "current", "stale", "unmanaged"];
const CLAUDE: readonly string[] = ["missing", "current", "noimport"];

/** `naut agents -check -json`'s stdout, or undefined when there is none to
 * read: a naut too old to have the command, or no naut at all. Either way
 * the feature is simply absent; nothing is offered. */
export function parseAgentsCheck(stdout: string): AgentsCheck | undefined {
  const line = stdout.trim().split(/\r?\n/).pop() ?? "";
  if (!line.startsWith("{")) return undefined;
  try {
    const v = JSON.parse(line) as Partial<AgentsCheck>;
    if (typeof v.agentsMd !== "string" || !AGENTS.includes(v.agentsMd)) return undefined;
    if (typeof v.claudeMd !== "string" || !CLAUDE.includes(v.claudeMd)) return undefined;
    return { agentsMd: v.agentsMd, claudeMd: v.claudeMd, version: typeof v.version === "string" ? v.version : "" };
  } catch {
    return undefined;
  }
}

export interface AgentsOffer {
  /** What the notification says. */
  message: string;
  /** The button that does it. */
  action: string;
  /** The naut arguments (after the binary) that do it; the project dir is
   * appended by the caller. */
  args: string[];
  /** Identifies this offer, so "Not now" silences only this one: a later
   * naut whose guidance changed again asks again. */
  key: string;
}

/** What to offer for a project, or undefined when there is nothing to do.
 * `name` is how the project is called in the message. */
export function agentsOffer(c: AgentsCheck, name: string): AgentsOffer | undefined {
  const who = "the guidance AI assistants (Claude Code, Copilot, Cursor, Codex) read before writing its programs";
  switch (c.agentsMd) {
    case "missing":
      return {
        message: `nautilus: ${name} has no AGENTS.md, ${who}. Add it?`,
        action: "Add AGENTS.md",
        args: ["agents"],
        key: "missing",
      };
    case "stale":
      return {
        message: `nautilus: ${name}'s AGENTS.md is older than your naut's guidance. Update it? Notes outside the nautilus block are kept.`,
        action: "Update AGENTS.md",
        args: ["agents"],
        key: `stale:${c.version}`,
      };
    case "unmanaged":
      return {
        message: `nautilus: ${name}'s AGENTS.md has none of nautilus's guidance for AI assistants. Add it below your text?`,
        action: "Add below my text",
        args: ["agents", "-append"],
        key: "unmanaged",
      };
  }
  if (c.claudeMd === "noimport") {
    return {
      message: `nautilus: ${name}'s CLAUDE.md does not import AGENTS.md, so Claude Code does not read the nautilus guidance. Add the import?`,
      action: "Add @AGENTS.md",
      args: ["agents"],
      key: "noimport",
    };
  }
  return undefined;
}

/** The workspaceState key that remembers a project's answer. */
export function rememberKey(dir: string): string {
  return `nautilus.agentsMd.answer:${dir}`;
}

/** Whether a remembered answer silences this offer: "never" silences every
 * offer for the project, "notnow:<key>" only the same offer. */
export function silenced(remembered: string | undefined, offer: AgentsOffer): boolean {
  return remembered === "never" || remembered === `notnow:${offer.key}`;
}
