import { describe, expect, it } from 'vitest';
import { formatDuration } from '../src/lib/format';

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
