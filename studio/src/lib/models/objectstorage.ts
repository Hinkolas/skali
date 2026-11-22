// The object storage page's stat tiles: buckets against what the store can
// hold, volume slots, what the buckets store, and the volume size limit.

import type { StatCardData } from '#lib/models/view.ts';
import { formatBytes, formatCount } from '#lib/format.ts';
import {
	formatVolumeSize,
	headroomState,
	type ObjectStore,
	type ObjectStoreBuckets
} from '#lib/types/objectstorage.ts';

const BAR: Record<ReturnType<typeof headroomState>, string> = {
	exhausted: 'bg-status-danger',
	low: 'bg-status-warning',
	ok: 'bg-service-storage',
	unknown: 'bg-service-storage'
};

export function objectStoreStats(
	store: ObjectStore,
	inventory: ObjectStoreBuckets | null
): StatCardData[] {
	const capacity = store.capacity;
	const state = headroomState(store);

	// Buckets against the estimated limit, the way connections draw
	// against max_connections: how many more can be created before a new
	// bucket's first write fails.
	let bucketsStat: StatCardData = {
		label: 'BUCKETS',
		value: formatCount(store.buckets),
		note: 'capacity unknown: the master did not answer'
	};
	if (capacity) {
		const limit = store.buckets + capacity.bucket_headroom;
		bucketsStat = {
			label: 'BUCKETS',
			value: formatCount(store.buckets),
			unit: `/ ~${formatCount(limit)}`,
			progress: { pct: limit > 0 ? (store.buckets / limit) * 100 : 100, class: BAR[state] },
			note:
				state === 'exhausted'
					? 'no room for another bucket'
					: `about ${formatCount(capacity.bucket_headroom)} more fit`
		};
	}

	let slotsStat: StatCardData = { label: 'VOLUME SLOTS', value: 'n/a', note: 'not read' };
	if (capacity) {
		slotsStat = {
			label: 'VOLUME SLOTS',
			value: formatCount(capacity.used_slots),
			unit: `/ ${formatCount(capacity.slots)}`,
			progress: {
				pct: capacity.slots > 0 ? (capacity.used_slots / capacity.slots) * 100 : 100,
				class: BAR[state]
			},
			split: [
				{ label: 'free', value: formatCount(capacity.free_slots) },
				{ label: 'per bucket', value: `${capacity.volumes_per_bucket * capacity.copies}` }
			]
		};
	}

	let storedStat: StatCardData = { label: 'STORED', value: 'n/a', note: 'usage not read' };
	if (inventory?.usage_observed) {
		const all = [
			...inventory.buckets.map((b) => b.usage),
			...inventory.unclaimed.map((c) => c.usage)
		];
		const used = all.reduce((sum, u) => sum + (u?.used_bytes ?? 0), 0);
		const deleted = all.reduce((sum, u) => sum + (u?.deleted_bytes ?? 0), 0);
		const parts = formatBytes(used).split(' ');
		storedStat = {
			label: 'STORED',
			value: parts[0],
			unit: parts[1],
			split: [
				{ label: 'awaiting vacuum', value: formatBytes(deleted) },
				...(capacity ? [{ label: 'copies', value: `×${capacity.copies}` }] : [])
			]
		};
	}

	const [size, sizeUnit] = formatVolumeSize(store.volume_size.limit_mb).split(' ');
	const volumeStat: StatCardData = {
		label: 'VOLUME SIZE',
		value: size,
		unit: sizeUnit,
		note: store.volume_size.default ? 'platform default' : 'set by an admin'
	};

	return [bucketsStat, slotsStat, storedStat, volumeStat];
}
