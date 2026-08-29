import type { ChartPoint } from '$lib/charts';

// Pure UI shapes shared across pages, independent of any data source.

export type ChipTone = 'success' | 'neutral' | 'warning';

export interface StatCardData {
	label: string;
	value: string;
	unit?: string;
	chip?: { text: string; tone: ChipTone };
	note?: string;
	progress?: { pct: number; class: string };
	/** Trend preview of the value over the loaded window; gaps are nulls. */
	sparkline?: ChartPoint[];
}
