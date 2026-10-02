// Stat tiles shared by the stateful services (databases and buckets): what
// they store against what they asked for, how they are backed up, who
// depends on them, and where their claim stands. Both overviews build the
// same four tiles from these so the two pages read alike.

import type { StatCardData } from '$lib/models/view';
import type { ServiceView } from '$lib/models/service';
import type { BackupSchedule } from '$lib/types/project';
import type { Run } from '$lib/types/runs';
import type { ServiceStatus } from '$lib/types/status';
import { describeCron, describeSeconds } from '$lib/cron';
import { formatBytes, relativeTime } from '$lib/format';

export type ClaimKind = 'database' | 'bucket';

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
 * Age of the newest successful backup run with the environment's schedule
 * and retention as sub stats. Snapshots are taken per environment, so the
 * environment's backup runs are every stateful service's backups.
 */
export function lastBackupStat(
	backup: BackupSchedule | null,
	runs: Run[] | null,
	noun: string
): StatCardData {
	const split: StatCardData['split'] = [];
	if (backup) {
		split.push({ label: 'schedule', value: describeCron(backup.schedule) });
		split.push({ label: 'keep', value: describeSeconds(backup.retention_seconds) });
	}
	const last = (runs ?? []).find((r) => r.kind === 'backup' && r.status === 'succeeded');
	if (last) {
		const [ago, ...rest] = relativeTime(last.finished_at ?? last.created_at).split(' ');
		return { label: 'LAST BACKUP', value: ago, unit: rest.join(' '), split };
	}
	return {
		label: 'LAST BACKUP',
		value: 'none',
		note: backup ? undefined : `automatic backups are off for this ${noun}'s environment`,
		split
	};
}

/** The applications that declared a dependency on this service. */
export function dependents(services: ServiceView[], kind: ClaimKind, key: string): ServiceView[] {
	const ref = `${kind === 'database' ? 'databases' : 'buckets'}.${key}`;
	return services.filter((s) => s.type === 'application' && s.dependencies.includes(ref));
}

/** The dependents tile. `pending` means the live status has not arrived
 * yet, so the note holds off the healthy count instead of reading 0/N. */
export function connectedStat(
	apps: ServiceView[],
	live: (key: string) => ServiceStatus | undefined,
	how: string,
	pending = false
): StatCardData {
	const healthy = apps.filter((a) => live(a.key)?.health === 'healthy').length;
	let note = `${healthy}/${apps.length} healthy · ${how}`;
	if (apps.length === 0) note = 'no application depends on it';
	else if (pending) note = `health pending · ${how}`;
	return {
		label: 'CONNECTED',
		value: `${apps.length}`,
		unit: `app${apps.length === 1 ? '' : 's'}`,
		note
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
