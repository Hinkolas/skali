// Stat tiles shared by the stateful services (databases and buckets): what
// they store against what they asked for, how they are backed up, who
// depends on them, and where their claim stands. Both overviews build the
// same four tiles from these so the two pages read alike.

import type { StatCardData } from '$lib/models/view';
import type { ServiceView } from '$lib/models/service';
import type { Backup } from '$lib/types/definition';
import type { Run } from '$lib/types/runs';
import type { ServiceStatus } from '$lib/types/status';
import { describeCron, describeSeconds } from '$lib/cron';
import { formatBytes, relativeTime } from '$lib/format';

export type ClaimKind = 'database' | 'bucket';

/** A named backup schedule from the project definition. */
export interface Schedule {
	name: string;
	backup: Backup;
}

/** The first schedule (by name) whose selection includes this service. */
export function backupSchedule(
	backups: Record<string, Backup>,
	kind: ClaimKind,
	key: string
): Schedule | null {
	const covers = (b: Backup) =>
		kind === 'database'
			? b.include.allDatabases || (b.include.databases ?? []).includes(key)
			: b.include.allBuckets || (b.include.buckets ?? []).includes(key);
	const hit = Object.entries(backups)
		.toSorted(([a], [b]) => a.localeCompare(b))
		.find(([, b]) => covers(b));
	return hit ? { name: hit[0], backup: hit[1] } : null;
}

/**
 * Measured footprint against the declared size when both exist; the
 * declared size alone before the sampler has seen the service; a bare
 * measurement when nothing was declared.
 */
export function footprintStat(
	label: string,
	used: number | null | undefined,
	declared: number | undefined,
	barClass: string,
	words: { declared: string; undeclared: string; measured: string }
): StatCardData {
	if (used != null) {
		const parts = formatBytes(used).split(' ');
		return declared
			? {
					label,
					value: parts[0],
					unit: `${parts[1]} / ${formatBytes(declared)}`,
					progress: { pct: Math.min(100, (used / declared) * 100), class: barClass }
				}
			: { label, value: parts[0], unit: parts[1], note: words.measured };
	}
	return {
		label,
		value: declared ? formatBytes(declared) : 'none',
		note: declared ? words.declared : words.undeclared
	};
}

/**
 * Age of the newest successful backup run with the schedule and retention
 * as sub stats. Snapshots are taken per environment, so the environment's
 * backup runs are every stateful service's backups.
 */
export function lastBackupStat(
	schedule: Schedule | null,
	runs: Run[] | null,
	noun: string
): StatCardData {
	const split: StatCardData['split'] = [];
	if (schedule) {
		split.push({ label: 'schedule', value: describeCron(schedule.backup.schedule) });
		split.push({ label: 'keep', value: describeSeconds(schedule.backup.retentionSeconds) });
	}
	const last = (runs ?? []).find((r) => r.kind === 'backup' && r.status === 'succeeded');
	if (last) {
		const [ago, ...rest] = relativeTime(last.finished_at ?? last.created_at).split(' ');
		return { label: 'LAST BACKUP', value: ago, unit: rest.join(' '), split };
	}
	return {
		label: 'LAST BACKUP',
		value: 'none',
		note: schedule ? undefined : `no schedule covers this ${noun}`,
		split
	};
}

/** The applications that declared a dependency on this service. */
export function dependents(services: ServiceView[], kind: ClaimKind, key: string): ServiceView[] {
	const ref = `${kind === 'database' ? 'databases' : 'buckets'}.${key}`;
	return services.filter((s) => s.type === 'application' && s.dependencies.includes(ref));
}

export function connectedStat(
	apps: ServiceView[],
	live: (key: string) => ServiceStatus | undefined,
	how: string
): StatCardData {
	const healthy = apps.filter((a) => live(a.key)?.health === 'healthy').length;
	return {
		label: 'CONNECTED',
		value: `${apps.length}`,
		unit: `app${apps.length === 1 ? '' : 's'}`,
		note:
			apps.length === 0
				? 'no application depends on it'
				: `${healthy}/${apps.length} healthy · ${how}`
	};
}

export function phaseStat(
	connection: { phase: string; credential_version?: number } | null
): StatCardData {
	return {
		label: 'PHASE',
		value: connection?.phase ?? 'unknown',
		chip:
			connection?.phase === 'provisioned'
				? { text: 'ready', tone: 'success' }
				: { text: 'settling', tone: 'neutral' },
		note: connection?.credential_version
			? `credentials v${connection.credential_version}`
			: undefined
	};
}
