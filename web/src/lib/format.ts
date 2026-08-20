// Shared value formatters for metrics and timestamps.

export function formatBytes(n: number): string {
	const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let i = 0;
	while (n >= 1024 && i < units.length - 1) {
		n /= 1024;
		i++;
	}
	return `${n >= 10 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`;
}

export const formatRate = (n: number) => `${formatBytes(n)}/s`;

/** "982" / "1.4k" / "2.1M" — compact counts for stat tiles. */
export function formatCount(n: number): string {
	if (n < 1000) return `${Math.round(n)}`;
	if (n < 1_000_000) {
		const k = n / 1000;
		return `${k >= 10 ? Math.round(k) : k.toFixed(1)}k`;
	}
	const m = n / 1_000_000;
	return `${m >= 10 ? Math.round(m) : m.toFixed(1)}M`;
}

/** "250m" below one core, "1.5" / "12" cores above (kubectl-style). A
 * decimal below 10m keeps tiny-usage axis ticks distinct ("0.5m" vs "1m"). */
export function formatCores(millicores: number): string {
	if (millicores < 10) return `${+millicores.toFixed(1)}m`;
	if (millicores < 1000) return `${Math.round(millicores)}m`;
	const cores = millicores / 1000;
	return cores >= 10 ? `${Math.round(cores)}` : cores.toFixed(1);
}

export const formatPct = (n: number) => `${Math.round(n)}%`;

/** "14:32" — sparse x-axis ticks. */
export function formatClock(t: number): string {
	return new Date(t).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

/** "Jul 6, 14:32" — tooltip header. */
export function formatTimestamp(t: number): string {
	return new Date(t).toLocaleString(undefined, {
		month: 'short',
		day: 'numeric',
		hour: '2-digit',
		minute: '2-digit'
	});
}

export function relativeTime(iso: string | null): string {
	if (!iso) return 'never';
	const secs = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
	if (secs < 60) return `${secs}s ago`;
	if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
	if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
	return `${Math.floor(secs / 86400)}d ago`;
}

/** "42s" / "3m 12s" between two instants; a missing end means "until now". */
export function formatDuration(startIso: string | null, endIso: string | null): string {
	if (!startIso) return '';
	const end = endIso ? new Date(endIso).getTime() : Date.now();
	const secs = Math.max(0, Math.round((end - new Date(startIso).getTime()) / 1000));
	if (secs < 60) return `${secs}s`;
	const mins = Math.floor(secs / 60);
	if (mins < 60) return `${mins}m ${secs % 60}s`;
	return `${Math.floor(mins / 60)}h ${mins % 60}m`;
}

/** "Jul 6, 2026, 14:32" — absolute timestamps in tables and settings. */
export function formatDateTime(iso: string | null): string {
	if (!iso) return '';
	return new Date(iso).toLocaleString(undefined, {
		year: 'numeric',
		month: 'short',
		day: 'numeric',
		hour: '2-digit',
		minute: '2-digit'
	});
}
