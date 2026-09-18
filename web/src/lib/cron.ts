// Human wording for the cron schedules an environment's backup setting accepts. Covers the
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
		if (minute === '*' && hour === '*') return 'every minute';
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

// A faithful reading of the five-field grammar skali accepts (the Go side in
// internal/cron is the authority): *, numbers, ranges, lists, steps, month
// and weekday names, day of week 7 as Sunday, and the classic rule that a
// day matches when either restricted day field does. Fires are computed in
// UTC because that is how skalid evaluates them.

const MONTHS: Record<string, number> = {
	jan: 1,
	feb: 2,
	mar: 3,
	apr: 4,
	may: 5,
	jun: 6,
	jul: 7,
	aug: 8,
	sep: 9,
	oct: 10,
	nov: 11,
	dec: 12
};
const WEEKDAYS: Record<string, number> = { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 };

interface CronField {
	min: number;
	max: number;
	names?: Record<string, number>;
}

const FIELDS: CronField[] = [
	{ min: 0, max: 59 },
	{ min: 0, max: 23 },
	{ min: 1, max: 31 },
	{ min: 1, max: 12, names: MONTHS },
	{ min: 0, max: 7, names: WEEKDAYS }
];

function parseValue(spec: CronField, text: string): number | null {
	const named = spec.names?.[text.toLowerCase()];
	if (named !== undefined) return named;
	if (!/^\d+$/.test(text)) return null;
	const n = Number(text);
	return n < spec.min || n > spec.max ? null : n;
}

function parseField(spec: CronField, field: string): Set<number> | null {
	const values = new Set<number>();
	for (const item of field.split(',')) {
		const [range, stepText] = item.split('/');
		let step = 1;
		if (stepText !== undefined) {
			if (!/^\d+$/.test(stepText) || Number(stepText) < 1) return null;
			step = Number(stepText);
		}
		let low = spec.min;
		let high = spec.max;
		if (range !== '*') {
			if (range.includes('-')) {
				const [a, b] = range.split('-');
				const lo = parseValue(spec, a);
				const hi = parseValue(spec, b);
				if (lo === null || hi === null || lo > hi) return null;
				low = lo;
				high = hi;
			} else {
				const v = parseValue(spec, range);
				if (v === null) return null;
				low = v;
				high = stepText !== undefined ? spec.max : v;
			}
		}
		for (let v = low; v <= high; v += step) values.add(v);
	}
	return values;
}

interface ParsedCron {
	minute: Set<number>;
	hour: Set<number>;
	dom: Set<number>;
	month: Set<number>;
	dow: Set<number>;
	domStar: boolean;
	dowStar: boolean;
}

function parseCron(expr: string): ParsedCron | null {
	const fields = expr.trim().split(/\s+/);
	if (fields.length !== 5) return null;
	const sets = fields.map((f, i) => parseField(FIELDS[i], f));
	if (sets.some((s) => s === null)) return null;
	const [minute, hour, dom, month, dow] = sets as Set<number>[];
	if (dow.has(7)) {
		dow.delete(7);
		dow.add(0);
	}
	return { minute, hour, dom, month, dow, domStar: fields[2] === '*', dowStar: fields[4] === '*' };
}

function dayMatches(c: ParsedCron, t: Date): boolean {
	const domHit = c.dom.has(t.getUTCDate());
	const dowHit = c.dow.has(t.getUTCDay());
	if (c.domStar && c.dowStar) return true;
	if (c.domStar) return dowHit;
	if (c.dowStar) return domHit;
	return domHit || dowHit;
}

/** Whether the expression is a five-field cron skalid would accept. */
export function isValidCron(expr: string): boolean {
	return parseCron(expr) !== null;
}

/**
 * The first fire strictly after `after`, in UTC, or null when the
 * expression is invalid or does not fire within five years.
 */
export function nextCronFire(expr: string, after: Date = new Date()): Date | null {
	const c = parseCron(expr);
	if (!c) return null;
	let t = new Date(Math.floor(after.getTime() / 60000) * 60000 + 60000);
	const limit = t.getTime() + 5 * 366 * 86400000;
	while (t.getTime() < limit) {
		if (!c.month.has(t.getUTCMonth() + 1)) {
			t = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth() + 1, 1));
			continue;
		}
		if (!dayMatches(c, t)) {
			t = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), t.getUTCDate() + 1));
			continue;
		}
		if (!c.hour.has(t.getUTCHours())) {
			t = new Date(
				Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), t.getUTCDate(), t.getUTCHours() + 1)
			);
			continue;
		}
		if (!c.minute.has(t.getUTCMinutes())) {
			t = new Date(t.getTime() + 60000);
			continue;
		}
		return t;
	}
	return null;
}
