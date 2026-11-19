// The port parameter client's contract (#705): a read says which device
// answered, a refused write changed nothing, and a write whose outcome is
// uncertain is neither a failure nor a success.
import { describe, it, expect } from './harness.js';
import {
	PortParamClient,
	canRead,
	canWrite,
	classify,
	failureText,
	identityText,
	identityTone,
	parseInput,
	valueLabel,
	type ParamInfo
} from '../src/lib/iolink.js';

type Sent = { url: string; method: string; headers: Record<string, string>; body: unknown };

function fake(answers: Array<Response | Error>) {
	const sent: Sent[] = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({
			url,
			method: init?.method ?? 'GET',
			headers: (init?.headers ?? {}) as Record<string, string>,
			body: init?.body ? JSON.parse(String(init.body)) : undefined
		});
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new PortParamClient({ fetch: f, token: 't' }) };
}
const json = (b: unknown, status = 200, headers: Record<string, string> = {}) => new Response(JSON.stringify(b), { status, headers });

const answer = { port: 'LEVEL1', variable: 'V_Setpoint', index: 64, subindex: 0, value: 100, raw: '0064', identity: 'Ok', identityChecked: true };
const setpoint: ParamInfo = { id: 'V_Setpoint', index: 64, ref: '64', access: 'rw', carried: true, type: 'UIntegerT', range: ['0..1000'] };
const mode: ParamInfo = {
	id: 'V_OutputMode',
	index: 66,
	ref: '66',
	access: 'rw',
	carried: true,
	type: 'UIntegerT',
	values: [
		{ value: '0', name: 'Off' },
		{ value: '1', name: 'Auto' }
	]
};

describe('requests', () => {
	it('reads and lists with the token, the port and the reference escaped', async () => {
		const { sent, client } = fake([json({ port: 'LEVEL1', master: 'M', params: [] }), json(answer)]);
		await client.list('LEVEL1');
		const r = await client.read('LEVEL1', 'Window.2');
		expect(sent[0].url).toBe('/api/iolink/LEVEL1/params');
		expect(sent[0].headers['X-Nautilus-Token']).toBe('t');
		expect(sent[1].url).toBe('/api/iolink/LEVEL1/params/Window.2');
		expect(r.ok).toBe(true);
	});
	it('writes with PUT and a JSON value', async () => {
		const { sent, client } = fake([json({ ...answer, value: 500 })]);
		const r = await client.write('LEVEL1', 'V_Setpoint', 500);
		expect(sent[0].method).toBe('PUT');
		expect(sent[0].body).toEqual({ value: 500 });
		expect(sent[0].headers['Content-Type']).toBe('application/json');
		expect(r.ok && (r.value.value as number)).toBe(500);
	});
});

describe('failures', () => {
	it('a refusal and an IODD refusal changed nothing, and are told apart', async () => {
		const a = await fake([json({ error: 'the device on the port is not the one expected (WrongDevice)' }, 409, { 'X-Nautilus-Outcome': 'refused' })]).client.write('L', 'V', 1);
		const b = await fake([json({ error: '5000 is outside the IODD\'s range' }, 422, { 'X-Nautilus-Outcome': 'refused' })]).client.write('L', 'V', 1);
		expect(!a.ok && a.kind).toBe('refused');
		expect(!b.ok && b.kind).toBe('invalid');
		expect(!a.ok && failureText(a.kind, a.message).includes('nothing was written')).toBe(true);
	});
	it('maps the other certain answers', () => {
		expect(classify(404, '{"error":"no port device"}', null, true).kind).toBe('notFound');
		expect(classify(401, 'unauthorized', null, true).kind).toBe('forbidden');
		expect(classify(403, 'no', null, true).kind).toBe('forbidden');
		expect(classify(400, 'expected {"value"}', null, true).kind).toBe('invalid');
		expect(classify(429, '{"error":"busy: an ISDU call is in flight; retry"}', null, true).kind).toBe('busy');
		expect(failureText('busy', 'x').includes('nothing was sent')).toBe(true);
	});
	it('an uncertain write is its own kind, from the body, the header, any 5xx and a lost connection', async () => {
		const body = classify(502, '{"error":"changed during the write","outcome":"unknown"}', null, true);
		expect(body.kind).toBe('uncertain');
		expect(body.message).toBe('changed during the write');
		expect(classify(502, 'x', 'unknown', true).kind).toBe('uncertain');
		// What the controller says about the outcome outranks the status.
		expect(classify(409, '{"error":"x","outcome":"unknown"}', null, true).kind).toBe('uncertain');
		expect(classify(409, 'x', 'unknown', true).kind).toBe('uncertain');
		expect(classify(502, 'the master timed out', null, true).kind).toBe('uncertain');
		expect(classify(503, 'standing by', null, true).kind).toBe('uncertain');
		const lost = await fake([new Error('network')]).client.write('L', 'V', 1);
		expect(!lost.ok && lost.kind).toBe('uncertain');
		const garbled = await fake([new Response('<html>', { status: 200 })]).client.write('L', 'V', 1);
		expect(!garbled.ok && garbled.kind).toBe('uncertain');
	});
	it('a read that fails is a failure, never uncertain', async () => {
		expect(classify(502, 'the master timed out', null, false).kind).toBe('failed');
		const lost = await fake([new Error('network')]).client.read('L', 'V');
		expect(!lost.ok && lost.kind).toBe('unreachable');
	});
	it('says "outcome is not known" for an uncertain write and never "failed"', () => {
		const t = failureText('uncertain', 'the device on the port changed during the write');
		expect(t.includes('not known')).toBe(true);
		expect(t.includes('Read it back')).toBe(true);
		expect(t.toLowerCase().includes('failed')).toBe(false);
	});
});

describe('what the panel shows', () => {
	it('lets a parameter be read or written by its rights and the master\'s reach', () => {
		expect(canRead(setpoint) && canWrite(setpoint)).toBe(true);
		expect(canWrite({ ...setpoint, access: 'ro' })).toBe(false);
		expect(canRead({ ...setpoint, access: 'wo' })).toBe(false);
		expect(canRead({ ...setpoint, carried: false }) || canWrite({ ...setpoint, carried: false })).toBe(false);
	});
	it('names a value where the IODD does', () => {
		expect(valueLabel(mode, 1)).toBe('Auto (1)');
		expect(valueLabel(mode, 7)).toBe('7');
		expect(valueLabel(setpoint, 500)).toBe('500');
		expect(valueLabel(setpoint, null)).toBe('');
	});
	it('says which device answered, and does not trust what it cannot check', () => {
		expect(identityTone('Ok', true)).toBe('ok');
		expect(identityTone('WrongDevice', true)).toBe('bad');
		expect(identityTone('NoDevice', true)).toBe('bad');
		expect(identityTone('Unchecked', true)).toBe('warn');
		expect(identityTone('Ok', false)).toBe('warn');
		expect(identityText('WrongDevice', true).includes('NOT the one expected')).toBe(true);
		expect(identityText('Ok', false).includes('not known which device')).toBe(true);
	});
	it('parses what is typed: numbers, names, true/false, text for a string parameter', () => {
		expect(parseInput(setpoint, '500')).toEqual({ ok: true, value: 500 });
		expect(parseInput(setpoint, '-3')).toEqual({ ok: true, value: -3 });
		expect(parseInput(setpoint, '2.5')).toEqual({ ok: true, value: 2.5 });
		expect(parseInput(mode, 'auto')).toEqual({ ok: true, value: 1 });
		expect(parseInput(setpoint, 'true')).toEqual({ ok: true, value: true });
		expect(parseInput({ ...setpoint, type: 'StringT' }, 'Pump 3')).toEqual({ ok: true, value: 'Pump 3' });
		expect(parseInput(setpoint, '').ok).toBe(false);
		expect(parseInput(setpoint, 'high').ok).toBe(false);
		expect(parseInput(setpoint, '18446744073709551615').ok).toBe(false);
	});
});
