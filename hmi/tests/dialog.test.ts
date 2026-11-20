// Modal's close-event rule (./dialog.ts): a late close event from an
// earlier close must not cancel the question now on screen. Found in a
// browser: a wrong password closed the dialog, the re-prompt opened at
// once, and the first close's event then cancelled it.
import { describe, it, expect } from './harness.js';
import { readFileSync } from 'node:fs';
import { closeIsReal } from '../src/lib/dialog.js';

describe('Modal — a native close event', () => {
	it('closes when the element really closed while the host had it open', () => {
		expect(closeIsReal(true, false)).toBe(true);
	});
	it('is ignored when the dialog is open again (an earlier close, arriving late)', () => {
		expect(closeIsReal(true, true)).toBe(false);
	});
	it('is ignored when the host already closed it', () => {
		expect(closeIsReal(false, false)).toBe(false);
	});
	it('Modal decides by it', () => {
		const src = readFileSync('src/lib/components/Modal.svelte', 'utf8');
		const handler = src.slice(src.indexOf('onclose={() => {'), src.indexOf('<!-- svelte-ignore'));
		expect(handler.includes('closeIsReal(open, !!dialogEl?.open)')).toBe(true);
	});
});
