// An in-memory file system with the Node-style callback API that Go's
// js/wasm port calls through globalThis.fs (see $GOROOT/src/syscall/fs_js.go),
// plus the process.cwd()/chdir() and path.resolve() it also needs. Loaded with
// importScripts before wasm_exec.js, so Go finds these instead of its ENOSYS
// stubs, and the language server, library resolution and project loader read
// the playground's project as if it were on disk.
//
// Classic-script global: self.createMemFS().
(function () {
	'use strict';

	const C = {
		O_RDONLY: 0, O_WRONLY: 1, O_RDWR: 2, O_CREAT: 64, O_EXCL: 128,
		O_TRUNC: 512, O_APPEND: 1024, O_DIRECTORY: 65536,
	};
	const S_IFDIR = 0o040000, S_IFREG = 0o100000;

	function err(code, path) {
		const e = new Error(code + (path ? ': ' + path : ''));
		e.code = code;
		return e;
	}

	function normalize(p) {
		const out = [];
		for (const part of p.split('/')) {
			if (part === '' || part === '.') continue;
			if (part === '..') out.pop();
			else out.push(part);
		}
		return '/' + out.join('/');
	}

	function parent(p) {
		const i = p.lastIndexOf('/');
		return i <= 0 ? '/' : p.slice(0, i);
	}

	self.createMemFS = function createMemFS(opts) {
		const log = (opts && opts.log) || ((fd, s) => (fd === 2 ? console.warn : console.log)(s));
		const enc = new TextEncoder();
		const dec = new TextDecoder();
		/** @type {Map<string, {dir: boolean, data: Uint8Array, mtime: number}>} */
		const nodes = new Map([['/', { dir: true, data: new Uint8Array(0), mtime: Date.now() }]]);
		const fds = new Map();
		let nextFd = 10;
		let cwd = '/';
		let ino = 1;
		const inos = new Map();
		const outBuf = { 1: '', 2: '' };
		const listeners = new Set(); // (path, kind) on every change Go makes

		const abs = (p) => normalize(p.startsWith('/') ? p : cwd + '/' + p);

		function statOf(path, n) {
			if (!inos.has(path)) inos.set(path, ino++);
			const size = n.dir ? 0 : n.data.length;
			return {
				dev: 1, ino: inos.get(path), mode: n.dir ? S_IFDIR | 0o755 : S_IFREG | 0o644,
				nlink: 1, uid: 0, gid: 0, rdev: 0, size, blksize: 4096,
				blocks: Math.ceil(size / 512), atimeMs: n.mtime, mtimeMs: n.mtime, ctimeMs: n.mtime,
				isDirectory: () => n.dir,
			};
		}

		function mkdirp(path) {
			path = abs(path);
			if (nodes.has(path)) return;
			mkdirp(parent(path));
			nodes.set(path, { dir: true, data: new Uint8Array(0), mtime: Date.now() });
		}

		function changed(path, kind) {
			for (const l of listeners) l(path, kind);
		}

		function stdout(fd, buf) {
			outBuf[fd] += dec.decode(buf);
			const nl = outBuf[fd].lastIndexOf('\n');
			if (nl !== -1) {
				log(fd, outBuf[fd].slice(0, nl));
				outBuf[fd] = outBuf[fd].slice(nl + 1);
			}
			return buf.length;
		}

		const fs = {
			constants: C,
			writeSync(fd, buf) {
				if (fd === 1 || fd === 2) return stdout(fd, buf);
				throw err('EBADF');
			},
			write(fd, buf, offset, length, position, cb) {
				if (fd === 1 || fd === 2) return cb(null, stdout(fd, buf.subarray(offset, offset + length)));
				const f = fds.get(fd);
				if (!f) return cb(err('EBADF'));
				const n = nodes.get(f.path);
				if (!n || n.dir) return cb(err('EISDIR', f.path));
				let pos = position == null ? (f.flags & C.O_APPEND ? n.data.length : f.pos) : position;
				const end = pos + length;
				if (end > n.data.length) {
					const grown = new Uint8Array(end);
					grown.set(n.data);
					n.data = grown;
				}
				n.data.set(buf.subarray(offset, offset + length), pos);
				n.mtime = Date.now();
				if (position == null) f.pos = end;
				f.dirty = true;
				cb(null, length);
			},
			read(fd, buf, offset, length, position, cb) {
				const f = fds.get(fd);
				if (!f) return cb(err('EBADF'));
				const n = nodes.get(f.path);
				if (!n) return cb(err('ENOENT', f.path));
				if (n.dir) return cb(err('EISDIR', f.path));
				const pos = position == null ? f.pos : position;
				const chunk = n.data.subarray(pos, Math.min(pos + length, n.data.length));
				buf.set(chunk, offset);
				if (position == null) f.pos = pos + chunk.length;
				cb(null, chunk.length);
			},
			open(path, flags, mode, cb) {
				path = abs(path);
				let n = nodes.get(path);
				if (n && flags & C.O_CREAT && flags & C.O_EXCL) return cb(err('EEXIST', path));
				if (!n) {
					if (!(flags & C.O_CREAT)) return cb(err('ENOENT', path));
					const p = nodes.get(parent(path));
					if (!p || !p.dir) return cb(err('ENOENT', path));
					n = { dir: false, data: new Uint8Array(0), mtime: Date.now() };
					nodes.set(path, n);
				}
				const writing = (flags & 3) !== C.O_RDONLY;
				if (n.dir && writing) return cb(err('EISDIR', path));
				if (!n.dir && flags & C.O_DIRECTORY) return cb(err('ENOTDIR', path));
				if (flags & C.O_TRUNC && !n.dir) n.data = new Uint8Array(0);
				const fd = nextFd++;
				fds.set(fd, { path, pos: 0, flags, dirty: !!(flags & (C.O_TRUNC | C.O_CREAT)) && writing });
				cb(null, fd);
			},
			close(fd, cb) {
				const f = fds.get(fd);
				if (!f) return cb(err('EBADF'));
				fds.delete(fd);
				if (f.dirty) changed(f.path, 'write');
				cb(null);
			},
			fstat(fd, cb) {
				const f = fds.get(fd);
				if (!f) return cb(err('EBADF'));
				const n = nodes.get(f.path);
				n ? cb(null, statOf(f.path, n)) : cb(err('ENOENT', f.path));
			},
			stat(path, cb) {
				path = abs(path);
				const n = nodes.get(path);
				n ? cb(null, statOf(path, n)) : cb(err('ENOENT', path));
			},
			lstat(path, cb) { fs.stat(path, cb); },
			readdir(path, cb) {
				path = abs(path);
				const n = nodes.get(path);
				if (!n) return cb(err('ENOENT', path));
				if (!n.dir) return cb(err('ENOTDIR', path));
				const prefix = path === '/' ? '/' : path + '/';
				const names = [];
				for (const k of nodes.keys()) {
					if (k !== path && k.startsWith(prefix) && !k.slice(prefix.length).includes('/')) names.push(k.slice(prefix.length));
				}
				cb(null, names.sort());
			},
			mkdir(path, perm, cb) {
				path = abs(path);
				if (nodes.has(path)) return cb(err('EEXIST', path));
				const p = nodes.get(parent(path));
				if (!p || !p.dir) return cb(err('ENOENT', path));
				nodes.set(path, { dir: true, data: new Uint8Array(0), mtime: Date.now() });
				cb(null);
			},
			unlink(path, cb) {
				path = abs(path);
				const n = nodes.get(path);
				if (!n) return cb(err('ENOENT', path));
				if (n.dir) return cb(err('EISDIR', path));
				nodes.delete(path);
				changed(path, 'delete');
				cb(null);
			},
			rmdir(path, cb) {
				path = abs(path);
				const n = nodes.get(path);
				if (!n) return cb(err('ENOENT', path));
				if (!n.dir) return cb(err('ENOTDIR', path));
				const prefix = path + '/';
				for (const k of nodes.keys()) if (k.startsWith(prefix)) return cb(err('ENOTEMPTY', path));
				nodes.delete(path);
				cb(null);
			},
			rename(from, to, cb) {
				from = abs(from); to = abs(to);
				if (!nodes.has(from)) return cb(err('ENOENT', from));
				const moves = [];
				for (const [k, v] of nodes) if (k === from || k.startsWith(from + '/')) moves.push([k, v]);
				for (const [k] of moves) nodes.delete(k);
				for (const [k, v] of moves) nodes.set(to + k.slice(from.length), v);
				changed(from, 'delete');
				changed(to, 'write');
				cb(null);
			},
			truncate(path, length, cb) {
				path = abs(path);
				const n = nodes.get(path);
				if (!n) return cb(err('ENOENT', path));
				n.data = n.data.slice(0, length);
				changed(path, 'write');
				cb(null);
			},
			ftruncate(fd, length, cb) {
				const f = fds.get(fd);
				if (!f) return cb(err('EBADF'));
				const n = nodes.get(f.path);
				n.data = n.data.slice(0, length);
				f.dirty = true;
				cb(null);
			},
			fsync(fd, cb) { cb(null); },
			chmod(path, mode, cb) { cb(null); },
			fchmod(fd, mode, cb) { cb(null); },
			chown(path, uid, gid, cb) { cb(null); },
			fchown(fd, uid, gid, cb) { cb(null); },
			lchown(path, uid, gid, cb) { cb(null); },
			utimes(path, atime, mtime, cb) { cb(null); },
			readlink(path, cb) { cb(err('EINVAL', path)); },
			link(path, link, cb) { cb(err('ENOSYS')); },
			symlink(path, link, cb) { cb(err('ENOSYS')); },
		};

		const process = {
			getuid: () => -1, getgid: () => -1, geteuid: () => -1, getegid: () => -1,
			getgroups() { throw err('ENOSYS'); },
			pid: -1, ppid: -1,
			umask() { return 0o022; },
			cwd: () => cwd,
			chdir(p) {
				p = abs(p);
				const n = nodes.get(p);
				if (!n) throw err('ENOENT', p);
				if (!n.dir) throw err('ENOTDIR', p);
				cwd = p;
			},
		};

		const path = { resolve: (...parts) => normalize(parts.reduce((acc, p) => (p.startsWith('/') ? p : acc + '/' + p), cwd)) };

		// The JavaScript side of the same files.
		const api = {
			writeFile(p, text) {
				p = abs(p);
				mkdirp(parent(p));
				nodes.set(p, { dir: false, data: typeof text === 'string' ? enc.encode(text) : text, mtime: Date.now() });
			},
			readFile(p) {
				const n = nodes.get(abs(p));
				if (!n || n.dir) return null;
				return dec.decode(n.data);
			},
			remove(p) {
				p = abs(p);
				for (const k of [...nodes.keys()]) if (k === p || k.startsWith(p + '/')) nodes.delete(k);
			},
			/** Every file under root, as {relativePath: text}. */
			snapshot(root) {
				root = abs(root);
				const out = {};
				for (const [k, n] of nodes) if (!n.dir && k.startsWith(root + '/')) out[k.slice(root.length + 1)] = dec.decode(n.data);
				return out;
			},
			mkdirp,
			onChange(fn) { listeners.add(fn); return () => listeners.delete(fn); },
		};

		return { fs, process, path, api };
	};
})();
