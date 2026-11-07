// The project folder VS Code shows (file:///project) is the engine's own
// in-memory file system: one store, so what the explorer shows, what the
// editor saves, what the language server reads and what Run compiles are
// the same files. Registered as a file:// overlay (files service override).
import { Emitter } from '@codingame/monaco-vscode-api/vscode/vs/base/common/event';
import {
	FileSystemProviderCapabilities,
	FileSystemProviderError,
	FileSystemProviderErrorCode,
	FileType,
	FileChangeType
} from '@codingame/monaco-vscode-files-service-override';

const enc = new TextEncoder();
const dec = new TextDecoder();

function toProviderError(e, path) {
	const code =
		e.code === 'ENOENT'
			? FileSystemProviderErrorCode.FileNotFound
			: e.code === 'EEXIST'
				? FileSystemProviderErrorCode.FileExists
				: e.code === 'EISDIR'
					? FileSystemProviderErrorCode.FileIsADirectory
					: e.code === 'ENOTDIR'
						? FileSystemProviderErrorCode.FileNotADirectory
						: FileSystemProviderErrorCode.Unknown;
	return FileSystemProviderError.create(`${e.message || e} (${path})`, code);
}

/**
 * A file system provider over pg's worker file system, under root.
 * onEdit runs after each change made from the workbench (an edit saved, a
 * file added, removed or renamed), not after the engine's own writes.
 */
export class EngineFileSystem {
	constructor(pg, root = '/project', onEdit = () => {}) {
		this.pg = pg;
		this.root = root;
		this.onEdit = onEdit;
		this.capabilities = FileSystemProviderCapabilities.FileReadWrite | FileSystemProviderCapabilities.PathCaseSensitive;
		this._onDidChangeFile = new Emitter();
		this.onDidChangeFile = this._onDidChangeFile.event;
		this.onDidChangeCapabilities = new Emitter().event;
		// Go writing a file (a test journal, say) shows up in the explorer.
		pg.on('fs', ({ path, kind }) => {
			if (!path || !path.startsWith(root)) return;
			this._onDidChangeFile.fire([{ type: kind === 'delete' ? FileChangeType.DELETED : FileChangeType.UPDATED, resource: { scheme: 'file', path } }]);
		});
	}

	/** Only paths under root belong to the engine; anything else is not ours. */
	owns(resource) {
		return resource.path === this.root || resource.path.startsWith(this.root + '/');
	}

	async fs(name, path, ...args) {
		try {
			return await this.pg.call('fsCall', name, path, ...args);
		} catch (e) {
			throw toProviderError(e, path);
		}
	}

	watch() {
		return { dispose() {} };
	}

	async stat(resource) {
		if (!this.owns(resource)) throw toProviderError({ code: 'ENOENT', message: 'not in the project' }, resource.path);
		const st = await this.fs('stat', resource.path);
		return { type: st.dir ? FileType.Directory : FileType.File, size: st.size, mtime: st.mtime, ctime: st.ctime };
	}

	async readdir(resource) {
		const names = await this.fs('readdir', resource.path);
		return Promise.all(
			names.map(async (n) => {
				const st = await this.fs('stat', resource.path + '/' + n);
				return [n, st.dir ? FileType.Directory : FileType.File];
			})
		);
	}

	async readFile(resource) {
		return enc.encode(await this.fs('readFile', resource.path));
	}

	async writeFile(resource, content) {
		await this.fs('writeFile', resource.path, dec.decode(content));
		this._onDidChangeFile.fire([{ type: FileChangeType.UPDATED, resource }]);
		this.onEdit();
	}

	async mkdir(resource) {
		await this.fs('mkdir', resource.path, 0o755);
		this._onDidChangeFile.fire([{ type: FileChangeType.ADDED, resource }]);
		this.onEdit();
	}

	async delete(resource, opts) {
		const st = await this.stat(resource);
		if (st.type === FileType.Directory) {
			if (opts && opts.recursive) await this.pg.call('remove', resource.path);
			else await this.fs('rmdir', resource.path);
		} else {
			await this.fs('unlink', resource.path);
		}
		this._onDidChangeFile.fire([{ type: FileChangeType.DELETED, resource }]);
		this.onEdit();
	}

	async rename(from, to) {
		await this.fs('rename', from.path, to.path);
		this._onDidChangeFile.fire([
			{ type: FileChangeType.DELETED, resource: from },
			{ type: FileChangeType.ADDED, resource: to }
		]);
		this.onEdit();
	}
}
