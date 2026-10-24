// Backup shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type BackupTrigger = 'manual' | 'scheduled';

/** One snapshot on the backup target, read from its manifest. */
export interface BackupSnapshot {
	id: string;
	/** Name of the environment the snapshot was taken from. */
	environment: string;
	environment_id?: string;
	orphaned?: boolean;
	created_at: string;
	revision_checksum: string;
	encryption: string;
	/** Manual snapshots are kept until deleted; scheduled ones expire under their environment's retention. */
	trigger: BackupTrigger;
	strategy: string;
	databases: number;
	buckets: number;
	volumes: number;
	bytes: number;
}

/** The external S3 backup location; the secret key is write-only and never returned. */
export interface BackupTarget {
	name: string;
	endpoint: string;
	region: string;
	bucket: string;
	prefix: string;
	access_key_id: string;
	created_at: string;
	updated_at: string;
}

/** One finished backup run; error is set on a failure. */
export interface BackupRunInfo {
	project: string;
	environment: string;
	finished_at: string;
	error?: string;
}

/** GET /v1/system/backup-target/summary: the target at a glance. */
export interface BackupTargetSummary {
	target: BackupTarget;
	reachable: boolean;
	/** Why the bucket did not answer; absent while it does. */
	unreachable_reason?: string;
	checked_at: string;
	/** What the bucket's snapshot manifests hold; null while it does not answer. */
	snapshots: {
		count: number;
		bytes: number;
		manual: number;
		scheduled: number;
		/** Snapshots of environments the control plane no longer has. */
		orphaned: number;
		oldest_at: string | null;
		newest_at: string | null;
		/** Largest first. */
		projects: { project: string; count: number; bytes: number; newest_at: string }[];
	} | null;
	runs: {
		/** Where the succeeded and failed counts start. */
		since: string;
		succeeded: number;
		failed: number;
		backing_up: number;
		restoring: number;
		last_succeeded: BackupRunInfo | null;
		last_failed: BackupRunInfo | null;
	};
	schedules: { environments: number; scheduled: number };
}

/** A failure newer than the newest success: backups need a look. */
export function failingNow(summary: BackupTargetSummary): boolean {
	const failed = summary.runs.last_failed;
	if (!failed) return false;
	const succeeded = summary.runs.last_succeeded;
	return !succeeded || Date.parse(failed.finished_at) > Date.parse(succeeded.finished_at);
}

export interface BackupTargetInput {
	endpoint: string;
	region: string;
	bucket: string;
	prefix: string;
	access_key_id: string;
	secret_access_key: string;
}

/** "scheduled" for a snapshot the environment's schedule took, "manual" otherwise. */
export function snapshotOrigin(snapshot: BackupSnapshot): string {
	return snapshot.trigger === 'scheduled' ? 'scheduled' : 'manual';
}

/** "1 database · 2 buckets · 1 volume" for a snapshot's contents. */
export function snapshotContents(snapshot: BackupSnapshot): string {
	const part = (n: number, noun: string) => (n ? `${n} ${noun}${n === 1 ? '' : 's'}` : '');
	const parts = [
		part(snapshot.databases, 'database'),
		part(snapshot.buckets, 'bucket'),
		part(snapshot.volumes, 'volume')
	].filter(Boolean);
	return parts.length ? parts.join(' · ') : 'empty';
}
