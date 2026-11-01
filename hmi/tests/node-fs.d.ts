// The one Node API the tests use (palette.test.ts reads the stylesheet);
// declared here rather than pulling @types/node into a browser package.
declare module 'node:fs' {
	export function readFileSync(path: string, encoding: 'utf8'): string;
}
