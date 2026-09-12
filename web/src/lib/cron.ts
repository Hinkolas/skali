// Human wording for the cron schedules the manifest accepts. Covers the
// shapes people actually write (every N minutes or hours, hourly, daily,
// weekly, monthly); anything else falls back to the raw expression so the
// reader still sees the truth.

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

function clock(hour: string, minute: string): string {
	return `${hour.padStart(2, '0')}:${minute.padStart(2, '0')}`;
}

function ordinal(n: number): string {
	const mod100 = n % 100;
	if (mod100 >= 11 && mod100 <= 13) return `${n}th`;
	switch (n % 10) {
		case 1:
			return `${n}st`;
		case 2:
			return `${n}nd`;
		case 3:
			return `${n}rd`;
		default:
			return `${n}th`;
	}
}

const NUM = /^\d+$/;

export function describeCron(expr: string): string {
	const fields = expr.trim().split(/\s+/);
	if (fields.length !== 5) return expr;
	const [minute, hour, dom, month, dow] = fields;

	if (month !== '*') return expr;

	const every = (field: string) => (field.startsWith('*/') ? Number(field.slice(2)) : null);

	if (dom === '*' && dow === '*') {
		const n = every(minute);
		if (n && hour === '*') return n === 1 ? 'every minute' : `every ${n} minutes`;
		const h = every(hour);
		if (h && NUM.test(minute)) return h === 1 ? 'hourly' : `every ${h} hours`;
		if (NUM.test(minute) && hour === '*') return `hourly at :${minute.padStart(2, '0')}`;
		if (NUM.test(minute) && NUM.test(hour)) return `daily at ${clock(hour, minute)}`;
		return expr;
	}
	if (dom === '*' && NUM.test(dow) && NUM.test(minute) && NUM.test(hour)) {
		const day = DAYS[Number(dow) % 7];
		return `weekly on ${day} at ${clock(hour, minute)}`;
	}
	if (dow === '*' && NUM.test(dom) && NUM.test(minute) && NUM.test(hour)) {
		return `monthly on the ${ordinal(Number(dom))} at ${clock(hour, minute)}`;
	}
	return expr;
}

/** "14 days" / "36 hours" / "45 minutes" for retention windows. */
export function describeSeconds(seconds: number): string {
	const unit = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
	if (seconds % 86400 === 0) return unit(seconds / 86400, 'day');
	if (seconds % 3600 === 0) return unit(seconds / 3600, 'hour');
	return unit(Math.round(seconds / 60), 'minute');
}
