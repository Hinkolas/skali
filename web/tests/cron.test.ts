import { describe, expect, it } from 'vitest';
import { describeCron, describeSeconds, nextCronFire } from '../src/lib/cron';

describe('describeCron', () => {
	it('reads the common schedules', () => {
		expect(describeCron('0 3 * * *')).toBe('daily at 03:00');
		expect(describeCron('30 14 * * *')).toBe('daily at 14:30');
		expect(describeCron('15 * * * *')).toBe('hourly at :15');
		expect(describeCron('0 */6 * * *')).toBe('every 6 hours');
		expect(describeCron('*/15 * * * *')).toBe('every 15 minutes');
		expect(describeCron('0 4 * * 0')).toBe('weekly on Sunday at 04:00');
		expect(describeCron('0 4 * * 7')).toBe('weekly on Sunday at 04:00');
		expect(describeCron('0 2 1 * *')).toBe('monthly on the 1st at 02:00');
		expect(describeCron('0 2 22 * *')).toBe('monthly on the 22nd at 02:00');
	});

	it('falls back to the expression it cannot word', () => {
		expect(describeCron('0 3 * 6 *')).toBe('0 3 * 6 *');
		expect(describeCron('0 3 1,15 * *')).toBe('0 3 1,15 * *');
		expect(describeCron('nonsense')).toBe('nonsense');
	});
});

describe('describeSeconds', () => {
	it('picks the largest exact unit', () => {
		expect(describeSeconds(1209600)).toBe('14 days');
		expect(describeSeconds(86400)).toBe('1 day');
		expect(describeSeconds(129600)).toBe('36 hours');
		expect(describeSeconds(2700)).toBe('45 minutes');
	});
});

describe('nextCronFire', () => {
	const at = (iso: string) => new Date(iso);
	it('finds the next fire in UTC', () => {
		expect(nextCronFire('0 3 * * *', at('2026-09-16T10:00:00Z'))?.toISOString()).toBe(
			'2026-09-17T03:00:00.000Z'
		);
		expect(nextCronFire('0 3 * * *', at('2026-09-16T02:59:30Z'))?.toISOString()).toBe(
			'2026-09-16T03:00:00.000Z'
		);
		expect(nextCronFire('*/15 * * * *', at('2026-09-16T10:16:00Z'))?.toISOString()).toBe(
			'2026-09-16T10:30:00.000Z'
		);
		expect(nextCronFire('30 7 * * mon', at('2026-09-16T00:00:00Z'))?.toISOString()).toBe(
			'2026-09-21T07:30:00.000Z'
		);
		expect(nextCronFire('0 0 29 2 *', at('2026-01-01T00:00:00Z'))?.toISOString()).toBe(
			'2028-02-29T00:00:00.000Z'
		);
		expect(nextCronFire('0 0 * * 7', at('2026-09-16T00:00:00Z'))?.toISOString()).toBe(
			'2026-09-20T00:00:00.000Z'
		);
		expect(nextCronFire('0 0 15 * fri', at('2026-09-16T00:00:00Z'))?.toISOString()).toBe(
			'2026-09-18T00:00:00.000Z'
		);
	});

	it('is strictly after the given instant', () => {
		expect(nextCronFire('0 3 * * *', at('2026-09-16T03:00:00Z'))?.toISOString()).toBe(
			'2026-09-17T03:00:00.000Z'
		);
	});

	it('returns null for invalid or never-firing expressions', () => {
		expect(nextCronFire('nonsense')).toBeNull();
		expect(nextCronFire('60 * * * *')).toBeNull();
		expect(nextCronFire('0 0 31 4 *', at('2026-01-01T00:00:00Z'))).toBeNull();
	});
});
