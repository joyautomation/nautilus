// The sliver of node:fs contract.test.ts uses. The package is a browser
// library and carries no @types/node (harness.ts says why).
declare module 'node:fs' {
	export function readFileSync(path: string, encoding: string): string;
	export function existsSync(path: string): boolean;
}
