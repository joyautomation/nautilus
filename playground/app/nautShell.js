// The playground's terminal: VS Code's own terminal panel (Ctrl+`, the +
// button, "Terminal: Create New Terminal"), backed by a small shell that
// runs in this page. There is no machine behind a browser tab, so it is not
// bash: it runs the `naut` verbs the engine has (test, run, stop, compose,
// ld|fbd|sfc graph, version), a few file commands over the project, and
// three playground commands for poking the running program (tags, set,
// watch). Everything else says it needs naut installed.
import { SimpleTerminalBackend, SimpleTerminalProcess } from '@codingame/monaco-vscode-terminal-service-override';
import { Emitter } from '@codingame/monaco-vscode-api/vscode/vs/base/common/event';

const ROOT = '/project';
const C = { dim: '\x1b[2m', bold: '\x1b[1m', red: '\x1b[31m', green: '\x1b[32m', yellow: '\x1b[33m', cyan: '\x1b[36m', reset: '\x1b[0m' };
const crlf = (s) => s.replace(/\r?\n/g, '\r\n');

const NAUT_VERBS = ['test', 'run', 'stop', 'compose', 'ld', 'fbd', 'sfc', 'version', 'check', 'help'];
const BUILTINS = ['help', 'ls', 'cat', 'pwd', 'clear', 'tags', 'set', 'watch', 'naut'];

const HELP = `${C.bold}nautilus playground shell${C.reset}  ${C.dim}(not bash: nautilus runs in this browser tab)${C.reset}

${C.bold}naut${C.reset}, as on your machine:
  naut test [-run <regex>] [-v]   run the acceptance suites (*_test.yaml) in virtual time
  naut run                        compile the saved files and start scanning (same as F5)
  naut stop                       stop the program
  naut compose <file> [--json]    the source the controller runs for a program
  naut ld|fbd|sfc graph <file>    a diagram's model, as the editors see it
  naut version

${C.bold}The running program${C.reset} (playground commands):
  tags [pattern]                  every tag's value, or those matching (Start*, *Level)
  set <tag> <value>               write a tag: set StartPB true, set InflowLps 40
  watch <tag> [<tag>...]          follow tags as they change; Ctrl+C stops

${C.bold}Files${C.reset}: ls [dir], cat <file>, pwd, clear
Keys: Up/Down for history, Tab completes, Ctrl+C cancels, Ctrl+L clears.

Everything else naut does (check, new, build, the device imports) needs
naut on your machine: https://nautilus.joyautomation.com/tutorial/
`;

/** Shell-style words: "a b" and 'a b' quoting, nothing fancier. */
function words(line) {
	const out = [];
	const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
	let m;
	while ((m = re.exec(line))) out.push(m[1] ?? m[2] ?? m[3]);
	return out;
}

function show(v) {
	if (v === null || v === undefined) return String(v);
	if (typeof v === 'number') return Number.isInteger(v) ? String(v) : String(Math.round(v * 1000) / 1000);
	if (typeof v === 'object') return JSON.stringify(v);
	return String(v);
}

function glob(pattern) {
	const re = '^' + pattern.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.') + '$';
	return new RegExp(re, 'i');
}

class NautShell extends SimpleTerminalProcess {
	constructor(pg, id, out, tries = []) {
		// A nonzero pid: VS Code forwards keystrokes only to a process whose
		// pid is truthy (terminalProcessManager: shellProcessId), so a shell
		// with pid 0 would queue every key as if it had not launched yet.
		super(id, id, ROOT, out.event);
		this.pg = pg;
		this.out = out;
		this.tries = tries;
		this.line = '';
		this.cursor = 0;
		this.history = [];
		this.hist = 0;
		this.busy = null; // {cancel()} while a command runs
		// The tab's title ("nautilus", not the default shell's name).
		this.props = new Emitter();
		this.onDidChangeProperty = this.props.event;
	}

	write(s) {
		this.out.fire(crlf(s));
	}

	prompt() {
		return `${C.cyan}project${C.reset} $ `;
	}

	redraw() {
		this.out.fire('\r\x1b[K' + this.prompt() + this.line + (this.cursor < this.line.length ? `\x1b[${this.line.length - this.cursor}D` : ''));
	}

	async start() {
		// SimpleTerminalProcess announces "ready" on a timer from its
		// constructor, which can fire before the terminal listens for it; a
		// terminal that never hears it drops every keystroke. Announce it
		// again now that start() is being awaited.
		setTimeout(() => {
			this.onReady.fire({ cwd: ROOT, pid: this.pid, windowsPty: undefined });
			this.props.fire({ type: 'title', value: 'nautilus' });
		});
		this.write(`${C.bold}nautilus playground shell${C.reset}: naut runs in this browser tab. Type ${C.bold}help${C.reset} for what it can do.\n`);
		if (this.tries.length) {
			this.write(`Try ${this.tries.map((t) => C.bold + t + C.reset).join(C.dim + ' · ' + C.reset)}\n`);
		}
		this.redraw();
		return undefined;
	}

	input(data) {
		if (this.busy) {
			if (data.includes('\x03')) this.busy.cancel();
			return;
		}
		for (let i = 0; i < data.length; i++) {
			const ch = data[i];
			const esc = data.slice(i, i + 3);
			if (esc === '\x1b[A' || esc === '\x1b[B') {
				i += 2;
				if (!this.history.length) continue;
				this.hist = Math.max(0, Math.min(this.history.length, this.hist + (esc === '\x1b[A' ? -1 : 1)));
				this.line = this.history[this.hist] ?? '';
				this.cursor = this.line.length;
				this.redraw();
			} else if (esc === '\x1b[C' || esc === '\x1b[D') {
				i += 2;
				this.cursor = Math.max(0, Math.min(this.line.length, this.cursor + (esc === '\x1b[C' ? 1 : -1)));
				this.redraw();
			} else if (ch === '\x1b') {
				// another escape sequence (Home, End, ...): skip it
				const m = data.slice(i).match(/^\x1b\[[0-9;]*[A-Za-z~]/);
				if (m) i += m[0].length - 1;
			} else if (ch === '\r') {
				const line = this.line;
				this.line = '';
				this.cursor = 0;
				this.out.fire('\r\n');
				if (line.trim()) {
					this.history.push(line);
					this.hist = this.history.length;
				}
				void this.exec(line);
				return; // the rest of a paste after Enter is dropped
			} else if (ch === '\x7f' || ch === '\b') {
				if (this.cursor > 0) {
					this.line = this.line.slice(0, this.cursor - 1) + this.line.slice(this.cursor);
					this.cursor--;
					this.redraw();
				}
			} else if (ch === '\x03') {
				this.out.fire('^C\r\n');
				this.line = '';
				this.cursor = 0;
				this.redraw();
			} else if (ch === '\x0c') {
				this.out.fire('\x1b[2J\x1b[H');
				this.redraw();
			} else if (ch === '\t') {
				void this.complete();
			} else if (ch >= ' ') {
				this.line = this.line.slice(0, this.cursor) + ch + this.line.slice(this.cursor);
				this.cursor++;
				this.redraw();
			}
		}
	}

	async complete() {
		const before = this.line.slice(0, this.cursor);
		const parts = before.split(/\s+/);
		const word = parts[parts.length - 1];
		let options;
		if (parts.length === 1) options = BUILTINS;
		else if (parts[0] === 'naut' && parts.length === 2) options = NAUT_VERBS;
		else if (parts[0] === 'set' || parts[0] === 'watch' || parts[0] === 'tags') options = Object.keys((await this.state()).tags ?? {});
		else options = Object.keys(await this.pg.files());
		const hits = options.filter((o) => o.startsWith(word));
		if (hits.length === 1) {
			const add = hits[0].slice(word.length) + ' ';
			this.line = this.line.slice(0, this.cursor) + add + this.line.slice(this.cursor);
			this.cursor += add.length;
			this.redraw();
		} else if (hits.length > 1) {
			this.write('\n' + hits.join('  ') + '\n');
			this.redraw();
		}
	}

	async state() {
		const r = await this.pg.api('GET', '/api/state');
		return r.status === 200 && r.body && typeof r.body === 'object' ? r.body : {};
	}

	async exec(line) {
		const argv = words(line);
		let cancelled = false;
		const cancels = [];
		this.busy = {
			cancel: () => {
				cancelled = true;
				for (const c of cancels) c();
			}
		};
		try {
			await this.run(argv, (c) => cancels.push(c), () => cancelled);
		} catch (e) {
			this.write(`${C.red}${(e && e.message) || e}${C.reset}\n`);
		} finally {
			this.busy = null;
			this.redraw();
		}
	}

	async run(argv, onCancel, cancelled) {
		const [cmd, ...args] = argv;
		switch (cmd) {
			case undefined:
				return;
			case 'help':
				return this.write(HELP);
			case 'clear':
				return this.out.fire('\x1b[2J\x1b[H');
			case 'pwd':
				return this.write(ROOT + '\n');
			case 'ls': {
				const files = Object.keys(await this.pg.files()).sort();
				const dir = (args[0] ?? '').replace(/^\.?\/?/, '').replace(/\/$/, '');
				const entries = new Set();
				for (const f of files) {
					if (dir && !f.startsWith(dir + '/')) continue;
					const rest = dir ? f.slice(dir.length + 1) : f;
					entries.add(rest.includes('/') ? rest.split('/')[0] + '/' : rest);
				}
				if (!entries.size) return this.write(`ls: ${args[0]}: no such directory\n`);
				return this.write([...entries].map((e) => (e.endsWith('/') ? `${C.cyan}${e}${C.reset}` : e)).join('  ') + '\n');
			}
			case 'cat': {
				if (!args.length) return this.write('cat: which file?\n');
				const files = await this.pg.files();
				for (const a of args) {
					const f = a.replace(/^\.?\//, '');
					if (!(f in files)) this.write(`cat: ${a}: no such file\n`);
					else this.write(files[f].endsWith('\n') ? files[f] : files[f] + '\n');
				}
				return;
			}
			case 'tags': {
				const st = await this.state();
				if (!st.tags) return this.write(`${C.yellow}No program is running: naut run${C.reset}\n`);
				const re = args[0] ? glob(args[0]) : null;
				const names = Object.keys(st.tags).filter((n) => !n.startsWith('__') && (!re || re.test(n))).sort();
				const w = Math.min(32, Math.max(...names.map((n) => n.length), 4));
				return this.write(names.map((n) => `${n.padEnd(w)}  ${show(st.tags[n])}`).join('\n') + (names.length ? '\n' : 'no tags match\n'));
			}
			case 'set': {
				if (args.length < 2) return this.write('set <tag> <value>, e.g. set StartPB true\n');
				const [name, raw] = [args[0], args.slice(1).join(' ')];
				let value = raw;
				if (/^(true|false)$/i.test(raw)) value = raw.toLowerCase() === 'true';
				else if (raw.trim() !== '' && !isNaN(Number(raw))) value = Number(raw);
				const r = await this.pg.api('POST', '/api/tags', { name, value });
				if (r.status >= 300) return this.write(`${C.red}set ${name}: ${typeof r.body === 'string' ? r.body.trim() : JSON.stringify(r.body)}${C.reset}\n`);
				return this.write(`${name} = ${show(value)}\n`);
			}
			case 'watch': {
				if (!args.length) return this.write('watch <tag> [<tag>...]; Ctrl+C stops\n');
				const res = args.map(glob);
				const last = new Map();
				this.write(`${C.dim}watching ${args.join(', ')}; Ctrl+C stops${C.reset}\n`);
				await new Promise((resolve) => {
					const stop = this.pg.stream('/api/stream', (ev) => {
						if (!ev) return resolve();
						let frame;
						try {
							frame = JSON.parse(ev.data);
						} catch {
							return;
						}
						for (const [n, v] of Object.entries(frame.tags ?? {})) {
							if (!res.some((r) => r.test(n))) continue;
							const s = show(v);
							if (last.get(n) === s) continue;
							last.set(n, s);
							const t = new Date(frame.ts ?? Date.now()).toLocaleTimeString();
							this.write(`${C.dim}${t}${C.reset}  ${n} = ${s}\n`);
						}
					});
					onCancel(() => {
						stop();
						this.write('^C\n');
						resolve();
					});
				});
				return;
			}
			case 'naut':
				return this.naut(args);
			default:
				if (['cd', 'git', 'npm', 'node', 'go', 'vim', 'nano', 'code'].includes(cmd)) {
					return this.write(`${cmd}: not in the playground (it runs in a browser tab, with no machine behind it). Type help.\n`);
				}
				return this.write(`${cmd}: command not found. Type help.\n`);
		}
	}

	async naut(args) {
		const [verb] = args;
		if (!verb || verb === 'help' || verb === '-h' || verb === '--help') return this.write(HELP);
		if (verb === 'run') {
			const r = await this.pg.run();
			for (const n of r.notices ?? []) this.write(`${C.yellow}notice:${C.reset} ${n}\n`);
			if (!r.ok) return this.write(`${C.red}naut run: ${r.error}${C.reset}\n`);
			return this.write(`${C.green}running${C.reset}: compiled in ${Math.round(r.ms)} ms. ${C.dim}tags, set and watch reach it; naut stop stops it.${C.reset}\n`);
		}
		if (verb === 'stop') {
			await this.pg.stop();
			return this.write('stopped\n');
		}
		if (verb === 'check') {
			return this.write(
				`naut check is not in the browser yet. The Problems panel (Ctrl+Shift+M) shows the language server's\n` +
					`diagnostics for every open file as you type, and naut run reports anything that does not compile.\n` +
					`On your machine: naut check .\n`
			);
		}
		const r = await this.pg.exec(args, '', ROOT);
		if (r.stdout) this.write(r.stdout.endsWith('\n') ? r.stdout : r.stdout + '\n');
		if (r.stderr) this.write(`${r.code ? C.red : ''}${r.stderr}${C.reset}`);
	}

	resize() {}
	clearBuffer() {}
	sendSignal(signal) {
		if (signal === 'SIGINT') this.busy?.cancel();
	}
	shutdown() {
		this.busy?.cancel();
	}
}

/** The terminal backend: every new terminal is a nautilus shell. */
export class NautShellBackend extends SimpleTerminalBackend {
	/** tries: the commands a new terminal suggests (the example's "try"). */
	constructor(pg, tries = []) {
		super();
		this.pg = pg;
		this.nextId = 1;
		this.getDefaultSystemShell = async () => 'nautilus';
		// The one profile, so the panel's profile picker reads "nautilus".
		this.getProfiles = async () => [{ profileName: 'nautilus', path: 'nautilus', isDefault: true, args: [] }];
		this.createProcess = async () => new NautShell(this.pg, this.nextId++, new Emitter(), tries);
		this.setReady();
	}
}
