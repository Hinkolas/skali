import { describe, expect, it } from 'vitest';
import { describeCron, describeSeconds } from '../src/lib/cron';

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
