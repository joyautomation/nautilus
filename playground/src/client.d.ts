// Types for client.js: the page's handle on the playground engine.

export declare const ROOT: '/project';

/** A project: {relative path: file text}. */
export type Files = Record<string, string>;

export interface LoadResult {
	ok: boolean;
	error?: string;
	/** What the browser changed to run it (simulated drivers, servers left off). */
	notices: string[];
	ms: number;
}

/** One `naut test --json` record (acceptance.Result), or {error}. */
export interface TestRecord {
	suite?: string;
	name?: string;
	line?: number;
	passed?: boolean;
	scans?: number;
	elapsedMs?: number;
	failure?: { step: number; line: number; stepLine?: number; atMs: number; reason: string; detail?: string };
	error?: string;
}

export interface ApiResponse<T = unknown> {
	status: number;
	headers: Record<string, string>;
	body: T;
}

export interface LspMessage {
	jsonrpc?: '2.0';
	id?: number;
	method?: string;
	params?: unknown;
	result?: unknown;
	error?: { code?: number; message: string };
}

export declare class Playground {
	/** Start the worker and wait for naut.wasm to be ready. */
	static start(opts: { base: string; version?: string }): Promise<Playground>;

	readonly version: string;
	readonly timing: Record<string, number> | null;

	call(op: string, ...args: unknown[]): Promise<unknown>;
	on(event: 'lsp', fn: (m: { msg: LspMessage }) => void): () => void;
	on(event: 'log', fn: (m: { fd: number; text: string }) => void): () => void;
	on(event: 'fs', fn: (m: { path: string; kind: 'write' | 'delete' }) => void): () => void;
	on(event: 'exit', fn: () => void): () => void;

	setProject(files: Files): Promise<void>;
	writeFile(rel: string, text: string): Promise<void>;
	removeFile(rel: string): Promise<void>;
	files(): Promise<Files>;

	run(): Promise<LoadResult>;
	stop(): Promise<void>;
	stats(): Promise<Record<string, unknown> | null>;
	api<T = unknown>(method: string, url: string, body?: unknown): Promise<ApiResponse<T>>;
	readonly fetch: typeof globalThis.fetch;
	stream(url: string, onEvent: (ev: { event: string; data: string } | null) => void): () => void;

	graph(lang: 'ld' | 'fbd' | 'sfc', src: string, at?: string): Promise<unknown>;
	edit(lang: 'ld' | 'fbd' | 'sfc', request: { source: string; op: unknown; file?: string }): Promise<{ out: unknown; code: number }>;
	lspStart(): Promise<void>;
	lspSend(msg: LspMessage): void;
	test(pattern?: string): Promise<TestRecord[]>;

	terminate(): void;
}

/** The file:// URI the language server knows a project file by. */
export declare function fileUri(rel: string): string;
