// Backup shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type BackupTrigger = 'manual' | 'scheduled';

/** One snapshot on the backup target, read from its manifest. */
export interface BackupSnapshot {
	id: string;
	/** Name of the environment the snapshot was taken from. */
	environment: string;
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
