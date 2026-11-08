// File search over the project, for workspace.findFiles and Quick Open.
//
// The search service override's own provider matches only Quick Open's
// fuzzy filePattern: the globs findFiles sends (includePattern,
// excludePattern) it ignores, so findFiles('**/Pump.svelte') answered with
// every file in the project. The extension finds its component sidecars,
// test manifests and custom components that way, so this one honours the
// whole query. Text search (the Search view) stays the override's.
import { match } from '@codingame/monaco-vscode-api/vscode/vs/base/common/glob';
import { fuzzyContains } from '@codingame/monaco-vscode-api/vscode/vs/base/common/strings';
import { URI } from '@codingame/monaco-vscode-api/vscode/vs/base/common/uri';

/** The relative path's glob checks: a query's own, and its folder's. */
function allowed(query, folderQuery, rel) {
	const ignoreCase = query.ignoreGlobCase || folderQuery.ignoreGlobCase;
	const opts = ignoreCase ? { ignoreCase: true } : undefined;
	const includes = [query.includePattern, folderQuery.includePattern].filter((p) => p && Object.keys(p).length);
	if (includes.some((p) => !match(p, rel, opts))) return false;
	const excludes = [query.excludePattern, ...(folderQuery.excludePattern ?? []).map((e) => e.pattern)].filter(Boolean);
	return !excludes.some((p) => match(p, rel, opts));
}

/**
 * A file search provider over the files `list()` returns (paths relative
 * to the folder at `root`, e.g. /project).
 */
export function projectFileSearch(list, root = '/project') {
	return {
		async getAIName() {
			return undefined;
		},
		async textSearch() {
			throw new Error('text search is not this provider');
		},
		async clearCache() {},
		async fileSearch(query, token) {
			const files = await list();
			const results = [];
			const max = query.exists ? 1 : query.maxResults || Infinity;
			for (const folderQuery of query.folderQueries) {
				if (URI.revive(folderQuery.folder).path.replace(/\/$/, '') !== root) continue;
				for (const rel of files) {
					if (token?.isCancellationRequested || results.length >= max) break;
					if (!allowed(query, folderQuery, rel)) continue;
					if (query.filePattern) {
						const hit = query.shouldGlobMatchFilePattern ? match(query.filePattern, rel) : fuzzyContains(rel, query.filePattern);
						if (!hit) continue;
					}
					results.push({ resource: URI.file(root + '/' + rel) });
				}
			}
			const limitHit = results.length >= max;
			return { results: query.exists ? [] : results, limitHit, messages: [] };
		}
	};
}
