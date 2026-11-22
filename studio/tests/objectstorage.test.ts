import { expect, test } from 'vitest';
import {
	estimateCapacity,
	formatVolumeSize,
	headroomState,
	parseVolumeSize,
	sortBuckets,
	storeSummary,
	volumeHeadroom,
	type ObjectStore,
	type ObjectStoreBucket,
	type ObjectStoreCapacity
} from '../src/lib/types/objectstorage';

function capacity(overrides: Partial<ObjectStoreCapacity> = {}): ObjectStoreCapacity {
	return {
		slots: 40,
		used_slots: 12,
		free_slots: 28,
		volume_headroom: 14,
		bucket_headroom: 14,
		volumes_per_bucket: 1,
		copies: 2,
		servers: [
			{
				url: '10.0.0.1:8080',
				slots: 20,
				used_slots: 6,
				free_slots: 14,
				free_disk_bytes: null,
				writable_volume_bytes: null
			},
			{
				url: '10.0.0.2:8080',
				slots: 20,
				used_slots: 6,
				free_slots: 14,
				free_disk_bytes: null,
				writable_volume_bytes: null
			}
		],
		...overrides
	};
}

function store(overrides: Partial<ObjectStore> = {}): ObjectStore {
	return {
		name: 'seaweed',
		state: 'active',
		image: 'chrislusf/seaweedfs:4.48',
		managed: true,
		masters: 3,
		volume_servers: 2,
		replication: '001',
		volume_size: { limit_mb: 256, default: true, default_mb: 256, min_mb: 64, max_mb: 30000 },
		buckets: 6,
		capacity: capacity(),
		observed: null,
		created_at: '2026-11-01T00:00:00Z',
		updated_at: '2026-11-01T00:00:00Z',
		...overrides
	};
}

function bucket(name: string, used: number | null, slots = 2): ObjectStoreBucket {
	return {
		bucket_name: name,
		owner: 'service',
		service_key: name,
		project: { id: 'p', name: 'demo', display_name: '' },
		environment: { id: 'e', name: 'production' },
		phase: 'provisioned',
		visibility: 'private',
		quota_bytes: 0,
		usage:
			used === null
				? null
				: {
						used_bytes: used,
						disk_bytes: used,
						deleted_bytes: 0,
						entry_count: 1,
						volumes: 1,
						slots
					},
		created_at: '2026-11-01T00:00:00Z'
	};
}

test('headroom state follows the buckets left', () => {
	expect(headroomState(store())).toBe('ok');
	expect(headroomState(store({ capacity: null }))).toBe('unknown');
	expect(headroomState(store({ capacity: capacity({ bucket_headroom: 0 }) }))).toBe('exhausted');
	expect(headroomState(store({ capacity: capacity({ volume_headroom: 0 }) }))).toBe('exhausted');
	// Fewer than five left always warns.
	expect(headroomState(store({ capacity: capacity({ bucket_headroom: 4 }) }))).toBe('low');
	// Under a tenth of everything the store can hold warns too.
	expect(headroomState(store({ buckets: 200, capacity: capacity({ bucket_headroom: 20 }) }))).toBe(
		'low'
	);
	expect(headroomState(store({ buckets: 100, capacity: capacity({ bucket_headroom: 20 }) }))).toBe(
		'ok'
	);
});

test('the store summary puts an outage before the headroom', () => {
	expect(storeSummary(store())).toMatchObject({ text: '6 buckets', tone: 'success' });
	expect(storeSummary(store({ capacity: capacity({ bucket_headroom: 0 }) }))).toMatchObject({
		tone: 'danger'
	});
	expect(storeSummary(store({ capacity: capacity({ bucket_headroom: 3 }) }))).toMatchObject({
		text: '3 buckets left',
		tone: 'warning'
	});
	const down = store({
		observed: {
			state: 'fresh',
			stale_since: null,
			masters_ready: 3,
			volume_servers_ready: 2,
			filer_ready: true,
			s3_ready: false,
			s3_detail: 'gateway down',
			under_replicated_volumes: 0,
			replication_pending_volumes: 0,
			volume_size_limit_mb: 256
		}
	});
	expect(storeSummary(down)).toMatchObject({ text: 'S3 not serving', tone: 'danger' });
});

test('volume headroom needs a different server for every copy', () => {
	expect(volumeHeadroom([5, 3], 1)).toBe(8);
	expect(volumeHeadroom([100, 3], 2)).toBe(3);
	expect(volumeHeadroom([4, 4, 4], 2)).toBe(6);
	expect(volumeHeadroom([10, 0], 2)).toBe(0);
	expect(volumeHeadroom([], 1)).toBe(0);
});

const MB = 1024 * 1024;

test('estimates follow the volume servers slot rule', () => {
	// The issue #289 cluster at 8 GB: four nearly empty volumes per server
	// reserve 32 GB against 16 GB of free disk, so no slot is left.
	const server = (url: string) => ({
		url,
		slots: 4,
		used_slots: 4,
		free_slots: 0,
		free_disk_bytes: 16 * 1024 * MB,
		writable_volume_bytes: [MB, MB, MB, MB]
	});
	const exhausted = capacity({
		free_slots: 0,
		bucket_headroom: 0,
		volume_headroom: 0,
		servers: ['a', 'b', 'c', 'd', 'e', 'f'].map(server)
	});
	// At 256 MB the four volumes reserve about 1 GB: (16 GB - 1020 MB) / 256 MB.
	const lowered = estimateCapacity(exhausted, 8000, 256)!;
	expect(lowered.rough).toBe(false);
	expect(lowered.freeSlots).toBe(6 * 60);
	expect(lowered.bucketHeadroom).toBe(180);

	// Without free disk the current free slots recover it: 14 free at
	// 256 MB with no writable volume is 14 * 256 MB, 28 slots at 128 MB.
	const recovered = capacity({
		servers: capacity().servers.map((s) => ({ ...s, writable_volume_bytes: [] }))
	});
	expect(estimateCapacity(recovered, 256, 128)).toEqual({
		freeSlots: 56,
		bucketHeadroom: 28,
		rough: false
	});
	// Unread volumes fall back to scaling the free slots, marked rough.
	expect(estimateCapacity(capacity(), 256, 1024)).toEqual({
		freeSlots: 6,
		bucketHeadroom: 3,
		rough: true
	});
	expect(estimateCapacity(capacity(), 256, 256)).toEqual({
		freeSlots: 28,
		bucketHeadroom: 14,
		rough: false
	});
	expect(estimateCapacity(null, 256, 128)).toBeNull();
});

test('volume sizes parse in MB or GB and print as admins read them', () => {
	expect(parseVolumeSize('256')).toBe(256);
	expect(parseVolumeSize(' 512 MB ')).toBe(512);
	expect(parseVolumeSize('1.5gb')).toBe(1500);
	expect(parseVolumeSize('1.5')).toBeNull();
	expect(parseVolumeSize('big')).toBeNull();
	expect(parseVolumeSize('0')).toBeNull();
	expect(formatVolumeSize(256)).toBe('256 MB');
	expect(formatVolumeSize(8000)).toBe('8 GB');
	expect(formatVolumeSize(1500)).toBe('1.5 GB');
});

test('buckets sort largest first, unread usage last', () => {
	const sorted = sortBuckets([
		bucket('b', 10),
		bucket('a', null),
		bucket('c', 500),
		bucket('d', 0, 0)
	]);
	expect(sorted.map((b) => b.bucket_name)).toEqual(['c', 'b', 'd', 'a']);
});
