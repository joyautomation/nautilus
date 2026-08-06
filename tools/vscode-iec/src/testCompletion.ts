// Tag-name completion inside *_test.yaml.
//
// A test names TAGS, not a program's locals — `given: { TempC: 45.0 }`
// refers to the tag store, so the useful completion set is whatever
// nautilus.yaml declares. The language server owns that knowledge (it
// reads the manifest to answer hover and VAR_EXTERNAL completion), so this
// asks it over a custom request rather than parsing YAML a second time in
// TypeScript against a different set of bugs.
//
// Scope: keys under `given:`, `expect:`, and `always:`. Elsewhere in the
// file a tag name means nothing, and offering one would be noise.

import * as vscode from "vscode";
import { LanguageClient } from "vscode-languageclient/node";

interface ProjectTag {
  name: string;
  role: string;
  type: string;
  unit?: string;
  desc?: string;
}

/** Keys whose contents name tags. */
const TAG_KEYS = /^\s*(given|expect|always)\s*:/;

/** A key line that ends the tag-naming region: any other test/step key. */
const OTHER_KEY = /^\s*(name|suspend|tolerance|steps|scans|advance|until|hold)\s*:/;

export function registerTestCompletion(client: LanguageClient): vscode.Disposable {
  const provider: vscode.CompletionItemProvider = {
    async provideCompletionItems(doc, position) {
      if (!doc.fileName.endsWith("_test.yaml")) return undefined;
      if (!namesTags(doc, position)) return undefined;

      let tags: ProjectTag[];
      try {
        tags = await client.sendRequest<ProjectTag[]>("nautilus/projectTags", {
          uri: doc.uri.toString(),
        });
      } catch {
        // The server may not be up yet, or this file may sit outside a
        // project. Either way, no completions beats an error popup.
        return undefined;
      }
      if (!tags?.length) return undefined;

      return tags.map((t) => {
        const item = new vscode.CompletionItem(t.name, vscode.CompletionItemKind.Field);
        item.detail = [t.type, t.role, t.unit].filter(Boolean).join(" · ");
        if (t.desc || t.role) {
          const md = new vscode.MarkdownString();
          if (t.desc) md.appendMarkdown(`${t.desc}\n\n`);
          if (t.role) md.appendMarkdown(`*${t.role}*`);
          item.documentation = md;
        }
        return item;
      });
    },
  };

  // YAML files only; the language id is "yaml" whether or not the YAML
  // extension is installed.
  return vscode.languages.registerCompletionItemProvider(
    [
      { language: "yaml", pattern: "**/*_test.yaml" },
      { language: "plaintext", pattern: "**/*_test.yaml" },
    ],
    provider,
  );
}

/**
 * Does the cursor sit somewhere a TAG name belongs?
 *
 * True on the value side of a `given:`/`expect:`/`always:` line, and on the
 * lines of a block beneath one — until a key that starts something else.
 * Walking back a bounded number of lines keeps this cheap and avoids
 * needing a YAML parser for what is a local question.
 */
function namesTags(doc: vscode.TextDocument, position: vscode.Position): boolean {
  const here = doc.lineAt(position.line).text;

  // `given: { Temp|` — inline flow mapping on the key's own line.
  if (TAG_KEYS.test(here)) {
    const beforeCursor = here.slice(0, position.character);
    return beforeCursor.includes("{") || /:\s*\S/.test(beforeCursor.replace(TAG_KEYS, ""));
  }

  const indent = here.search(/\S|$/);
  for (let line = position.line - 1; line >= 0 && position.line - line <= 40; line--) {
    const text = doc.lineAt(line).text;
    if (!text.trim()) continue;
    const thisIndent = text.search(/\S|$/);
    // A line at or left of ours closes the block we might have been in.
    if (thisIndent >= indent) continue;
    if (TAG_KEYS.test(text)) return true;
    if (OTHER_KEY.test(text) || /^\s*-\s/.test(text)) return false;
    return false;
  }
  return false;
}
