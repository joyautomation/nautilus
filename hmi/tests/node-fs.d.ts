// The Node APIs the tests use (palette.test.ts reads the stylesheet; the
// config tests walk examples/ and the schema); declared here rather than
// pulling @types/node into a browser package.
declare module 'node:fs' {
	export function readFileSync(path: string, encoding: 'utf8'): string;
	export function readdirSync(path: string): string[];
	export function existsSync(path: string): boolean;
}
