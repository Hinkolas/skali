// The object store as GET /v1/system/object-storage and its buckets route
// serve it, plus the presentation rules the System pages share.

export interface ObjectStoreVolumeSize {
	limit_mb: number;
	/** The limit is the platform default; no admin set one. */
	default: boolean;
	default_mb: number;
	min_mb: number;
	max_mb: number;
}

export interface ObjectStoreServer {
	url: string;
	node?: string;
	slots: number;
	used_slots: number;
	free_slots: number;
	/** The node's newest sampled free disk; null when unknown. */
	free_disk_bytes: number | null;
	/** Sizes of the server's writable volumes; null when unread. */
	writable_volume_bytes: number[] | null;
}

/** The volume servers' slot accounting, read from the master on request. */
export interface ObjectStoreCapacity {
	slots: number;
	used_slots: number;
	free_slots: number;
	/** More volumes that can grow with every copy on a different server. */
	volume_headroom: number;
	/** More buckets that fit before a new bucket's first write finds no room. */
	bucket_headroom: number;
	volumes_per_bucket: number;
	copies: number;
	servers: ObjectStoreServer[];
}

export interface ObjectStoreObserved {
	state: 'fresh' | 'stale' | 'unknown';
	stale_since: string | null;
	masters_ready: number;
	volume_servers_ready: number;
	filer_ready: boolean;
	s3_ready: boolean;
	s3_detail?: string;
	under_replicated_volumes: number;
	replication_pending_volumes: number;
	volume_size_limit_mb: number;
}

export interface ObjectStore {
	name: string;
	state: string;
	image: string;
	managed: boolean;
	masters: number;
	volume_servers: number;
	replication: string;
	volume_size: ObjectStoreVolumeSize;
	buckets: number;
	capacity: ObjectStoreCapacity | null;
	observed: ObjectStoreObserved | null;
	created_at: string;
	updated_at: string;
}

export interface ObjectStoreUsage {
	used_bytes: number;
	disk_bytes: number;
	deleted_bytes: number;
	entry_count: number;
	volumes: number;
	/** Volume copies: the slots the bucket holds. */
	slots: number;
}

export interface ObjectStoreBucket {
	bucket_name: string;
	owner: 'service' | 'system';
	system_key?: string;
	service_key?: string;
	project: { id: string; name: string; display_name: string } | null;
	environment: { id: string; name: string } | null;
	phase: string;
	visibility: 'private' | 'public-read';
	/** 0 means no quota. */
	quota_bytes: number;
	usage: ObjectStoreUsage | null;
	created_at: string;
}

export interface ObjectStoreCollection {
	collection: string;
	usage: ObjectStoreUsage;
}

export interface ObjectStoreBuckets {
	buckets: ObjectStoreBucket[];
	unclaimed: ObjectStoreCollection[];
	usage_observed: boolean;
}

export type Tone = 'success' | 'warning' | 'danger' | 'neutral';

/** The share of bucket capacity left below which the page warns. */
const LOW_HEADROOM_SHARE = 0.1;
/** Fewer buckets left than this always warns, whatever the share. */
const LOW_HEADROOM_BUCKETS = 5;

/**
 * How close the store is to refusing new buckets: exhausted when no new
 * bucket fits, low when few do (under five, or under a tenth of what the
 * store holds in all), fine otherwise; unknown without a capacity reading.
 */
export function headroomState(
	store: Pick<ObjectStore, 'buckets' | 'capacity'>
): 'exhausted' | 'low' | 'ok' | 'unknown' {
	const capacity = store.capacity;
	if (!capacity) return 'unknown';
	if (capacity.volume_headroom === 0 || capacity.bucket_headroom === 0) return 'exhausted';
	const total = store.buckets + capacity.bucket_headroom;
	if (
		capacity.bucket_headroom < LOW_HEADROOM_BUCKETS ||
		capacity.bucket_headroom < total * LOW_HEADROOM_SHARE
	) {
		return 'low';
	}
	return 'ok';
}

/** The pill next to the page title and on the System hub card. */
export function storeSummary(store: ObjectStore): { text: string; tone: Tone; title?: string } {
	const observed = store.observed;
	if (observed && observed.state === 'fresh' && !observed.s3_ready) {
		return { text: 'S3 not serving', tone: 'danger', title: observed.s3_detail };
	}
	switch (headroomState(store)) {
		case 'exhausted':
			return {
				text: 'no room for buckets',
				tone: 'danger',
				title: 'New buckets cannot be written to'
			};
		case 'low':
			return {
				text: `${store.capacity!.bucket_headroom} buckets left`,
				tone: 'warning',
				title: 'The store is close to its bucket limit'
			};
		case 'unknown':
			return { text: bucketCount(store.buckets), tone: 'neutral' };
	}
	if (observed?.state === 'fresh' && storeDegraded(store)) {
		return { text: 'degraded', tone: 'warning' };
	}
	return { text: bucketCount(store.buckets), tone: 'success' };
}

/** Whether the store serves with less than its recorded shape. */
export function storeDegraded(store: ObjectStore): boolean {
	const observed = store.observed;
	if (!observed) return false;
	return (
		observed.masters_ready < store.masters ||
		observed.volume_servers_ready < store.volume_servers ||
		!observed.filer_ready ||
		observed.under_replicated_volumes > 0 ||
		observed.replication_pending_volumes > 0
	);
}

export function bucketCount(n: number): string {
	return n === 1 ? '1 bucket' : `${n} buckets`;
}

/** "256 MB", "1 GB", "1.5 GB": the engine's megabytes, as an admin reads them. */
export function formatVolumeSize(mb: number): string {
	if (mb >= 1000 && mb % 1000 === 0) return `${mb / 1000} GB`;
	if (mb >= 1000) return `${+(mb / 1000).toFixed(1)} GB`;
	return `${mb} MB`;
}

/** SeaweedFS megabytes: the master multiplies the flag by 1024 * 1024. */
const MB = 1024 * 1024;

/** The room a server's writable volumes may still grow into under a limit. */
function reservedBytes(writable: number[], limitMb: number): number {
	const limit = limitMb * MB;
	return writable.reduce((sum, size) => sum + Math.max(0, limit - size), 0);
}

/**
 * A server's free slots under another volume size limit, by the rule the
 * volume servers apply: their volumes plus (free disk - the room their
 * writable volumes may still grow into) / limit, at least one. The free
 * disk is the node's sample, or else recovered from the current slots
 * (exact to within one volume). Null when neither is known.
 */
function serverFreeSlots(
	server: ObjectStoreServer,
	currentMb: number,
	nextMb: number
): number | null {
	const writable = server.writable_volume_bytes;
	if (!writable) return null;
	let freeDisk = server.free_disk_bytes;
	if (freeDisk == null) {
		if (server.slots <= server.used_slots) return null;
		freeDisk =
			(server.slots - server.used_slots) * currentMb * MB + reservedBytes(writable, currentMb);
	}
	const unclaimed = freeDisk - reservedBytes(writable, nextMb);
	const slots = Math.max(
		1,
		server.used_slots + (unclaimed > 0 ? Math.floor(unclaimed / (nextMb * MB)) : 0)
	);
	return Math.max(0, slots - server.used_slots);
}

/**
 * Estimates the capacity under another volume size limit, server by
 * server (serverFreeSlots); a server whose free disk is unknown falls back
 * to scaling its free slots by current/next, which leaves out the room
 * its volumes reserve, and marks the estimate rough. Null without a
 * reading or a valid limit.
 */
export function estimateCapacity(
	capacity: ObjectStoreCapacity | null,
	currentMb: number,
	nextMb: number
): { freeSlots: number; bucketHeadroom: number; rough: boolean } | null {
	if (!capacity || !(currentMb > 0) || !(nextMb > 0)) return null;
	if (nextMb === currentMb) {
		return {
			freeSlots: capacity.free_slots,
			bucketHeadroom: capacity.bucket_headroom,
			rough: false
		};
	}
	let rough = false;
	const free = capacity.servers.map((server) => {
		const exact = serverFreeSlots(server, currentMb, nextMb);
		if (exact != null) return exact;
		rough = true;
		return Math.floor((server.free_slots * currentMb) / nextMb);
	});
	const copies = Math.max(1, capacity.copies);
	return {
		freeSlots: free.reduce((sum, f) => sum + f, 0),
		bucketHeadroom: Math.floor(
			volumeHeadroom(free, copies) / Math.max(1, capacity.volumes_per_bucket)
		),
		rough
	};
}

/**
 * The largest n with sum(min(free_i, n)) >= copies * n: how many volumes
 * can grow with every copy on a different server (the server's rule,
 * mirrored here for estimates).
 */
export function volumeHeadroom(free: number[], copies: number): number {
	const open = free.filter((f) => f > 0);
	if (open.length < copies) return 0;
	let lo = 0;
	let hi = Math.floor(open.reduce((sum, f) => sum + f, 0) / copies);
	while (lo < hi) {
		const n = Math.floor((lo + hi + 1) / 2);
		const sum = open.reduce((acc, f) => acc + Math.min(f, n), 0);
		if (sum >= copies * n) lo = n;
		else hi = n - 1;
	}
	return lo;
}

/** Parses the volume size input: whole megabytes, or a figure with GB. */
export function parseVolumeSize(input: string): number | null {
	const match = input.trim().match(/^(\d+(?:\.\d+)?)\s*(mb|gb)?$/i);
	if (!match) return null;
	const value = Number(match[1]);
	const mb = match[2]?.toLowerCase() === 'gb' ? value * 1000 : value;
	return Number.isInteger(mb) && mb > 0 ? mb : null;
}

/** Who owns a bucket, as the bucket list shows it. */
export function bucketOwner(bucket: ObjectStoreBucket): string {
	if (bucket.owner === 'system') return `system · ${bucket.system_key ?? ''}`;
	const project = bucket.project?.display_name || bucket.project?.name || 'unknown project';
	return bucket.environment ? `${project} · ${bucket.environment.name}` : project;
}

/** Largest first by stored bytes; unread usage last, then by name. */
export function sortBuckets(buckets: ObjectStoreBucket[]): ObjectStoreBucket[] {
	return buckets.toSorted(
		(a, b) =>
			(b.usage?.used_bytes ?? -1) - (a.usage?.used_bytes ?? -1) ||
			(b.usage?.slots ?? -1) - (a.usage?.slots ?? -1) ||
			a.bucket_name.localeCompare(b.bucket_name)
	);
}

/** The phase pill of a bucket claim. */
export function bucketPhase(phase: string): { text: string; tone: Tone } {
	if (phase === 'provisioned') return { text: phase, tone: 'success' };
	if (phase === 'releasing' || phase === 'released') return { text: phase, tone: 'warning' };
	return { text: phase, tone: 'neutral' };
}
