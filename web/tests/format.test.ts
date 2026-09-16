import { describe, expect, it } from 'vitest';
import { describeActor, formatDuration, shortId } from '../src/lib/format';

describe('describeActor', () => {
	const ada = '4f2a9c1e-7b3d-4e8a-9f01-23456789abcd';
	const directory = new Map([[ada, { name: 'Ada Lovelace', email: 'ada@seed.skali.local' }]]);
	const resolve = (id: string) => directory.get(id);

	it('names the scheduler and the reconciler', () => {
		expect(describeActor('schedule:daily')).toBe('schedule · daily');
		expect(describeActor('system:reconcile')).toBe('system');
	});

	it('resolves a user id to the name, then the email', () => {
		expect(describeActor(ada, resolve)).toBe('Ada Lovelace');
		expect(describeActor(ada, () => ({ name: '', email: 'ada@seed.skali.local' }))).toBe(
			'ada@seed.skali.local'
		);
	});

	it('falls back to a short id for unknown users and without a directory', () => {
		expect(describeActor(ada, () => undefined)).toBe('4f2a9c1e');
		expect(describeActor(ada)).toBe('4f2a9c1e');
	});

	it('passes anything else through unchanged', () => {
		expect(describeActor('ada@seed.skali.local')).toBe('ada@seed.skali.local');
	});
});

describe('shortId', () => {
	it('keeps the first eight characters', () => {
		expect(shortId('4f2a9c1e-7b3d-4e8a-9f01-23456789abcd')).toBe('4f2a9c1e');
	});
});

describe('formatDuration', () => {
	const start = '2026-09-13T10:00:00Z';

	it('measures a finished span between its two instants', () => {
		expect(formatDuration(start, '2026-09-13T10:03:12Z')).toBe('3m 12s');
		expect(formatDuration(start, '2026-09-13T11:05:00Z')).toBe('1h 5m');
	});

	it('measures an open span up to the clock it is given', () => {
		const now = Date.parse('2026-09-13T10:00:42Z');
		expect(formatDuration(start, null, now)).toBe('42s');
		expect(formatDuration(start, null, now + 1000)).toBe('43s');
	});

	it('never runs backwards and stays empty without a start', () => {
		expect(formatDuration(start, '2026-09-13T09:59:00Z')).toBe('0s');
		expect(formatDuration(null, null)).toBe('');
	});
});
